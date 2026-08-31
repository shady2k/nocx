# The helper as execution host: one shape everywhere, and an update stops killing work

## 0. What a user can do that they could not before

Three things, and they are one mechanism:

1. Start a three-hour build on a remote host, let nocx update itself, and come back to a
   tab still driving the same running process.
2. Open the same workspace from a desktop and a laptop and see the same live sessions from
   both, at the same time.
3. The same, locally: quit nocx, update it, reopen it, and the local pane is still running.

The check that watches (1): start a marker-per-second long command in a tab on a remote
host; replace the coordinator with a build carrying a different helper version; the marker
timestamps span the update with no gap, the tab still reads and writes that same process,
and a NEW tab on that host comes up on the new generation. Close the old tab; the old
generation's directory is reclaimed.

The check that watches (2): attach the same workspace from two coordinators; both see the
same sessions and the same bytes; each resizes to whichever viewer is focused on that pane;
closing one leaves the other unaffected, and a coordinator that stops acking does not
throttle the other.

## 1. In one sentence

`nocx-helper` becomes the **execution host** — one per machine where work runs, per
generation: it spawns processes, owns their PTYs, applies OS policy, holds the replay ring
and stores opaque durable bytes, on the local machine and on remote hosts alike, over any
carrier — while the coordinator keeps every product decision and every interpretation; and
because a generation is an immutable content-addressed directory, generations coexist, so
updating nocx never kills work.

## 2. What this crosses, and what those documents already decided

**AD-1** (`architecture.md:104`) — one WebSocket, raw binary data plane, JSON-RPC control
plane, PTY bytes never inside JSON-RPC. It does **not** license an extra process hop: it
governs framing, never copies, wakeups or head-of-line boundaries. §7 answers the hop on
its own terms.

**AD-5** (`architecture.md:135`) — "Tier B ... **augments** (never replaces) the remote
shell." A helper that spawns the shell replaces the _delivery_ of the hooks. `nocx-k6p18`
already declares this amendment must land in `docs/architecture.md`; this design carries it
(D2), and narrows it: the hooks keep their role as the source of prompt and block metadata.

**AD-6** (`architecture.md:150`, `:151`) — the backend does not derive render state from
the byte stream, with **two** carve-outs, not one: ADR-0024's bootstrap window (a framing
we wrote, interpreting nothing), and ADR-0041's **live backend VT grid for an enrolled
pane, which does read the stream's meaning** — permitted because ADR-0002's revisit trigger
fired on "a session that survives the client process entirely". Both are coordinator-side.
D7 keeps them there and widens neither.

**AD-7** (`architecture.md:166`) — the backend session registry is authoritative;
`session.go:484` mints the id today. With two coordinators attached to one session, neither
is "the server". **This design amends AD-7 explicitly** (D6): the execution host mints
session identity, and the durable handle names its generation.

**AD-8** — interface-first, one owner per behaviour. Decisive three times: it forbids a
second implementation of the replay contract (D7); it says one owner per _behaviour_, not
one process per role, which is why there is no router (D6); and it forbids a second
description format for a surface `contracts/` already covers (D12).

**AD-9 / AD-10** (`architecture.md:181`, `:188`) — the replay ring, offsets, acks, reset;
losslessness with throttling at the bound. `internal/transport/ring.go`: `RingCapacity`
256 KiB, `CreditLimit` 64 KiB, `FairChunk` 8 KiB. D7 moves the ring's _instances_ and keeps
its _implementation_ single; D8 generalises its cursors from two to N.

**ADR-0018 / ADR-0021 / ADR-0043** — encryption at rest, secrets in the prompt, one
connection to the encrypted store (and that ADR is explicit it does not establish
cross-process safety). Together they are why the ledger does not move (D11): entries carry
command text, so a ledger in the helper puts the key on the far host.

**ADR-0019 — one authoritative ledger, disposable projections.** Its own Related line reads
"AD-1 as amended (**frontend-derived** ledger facts cross the control plane)" and "AD-6".
Block facts are derived in the renderer, which owns the grid and sees the markers, and
travel inward over the control plane. The helper sits below the coordinator and never sees
the renderer. D11 applies this ADR's title literally and says where it is being extended
rather than merely applied.

**ADR-0034** — consent keyed to the machine, not to a connection or daemon instance. Kept;
D14 changes only what uninstall does to the grant.

**The nocx-server design** (`.internal/specs/2026-08-28-the-nocx-server-design.md`):

- **D1** — two build targets, separate composition roots, because `//go:embed all:artifacts`
  means one artifact serving both roles would embed itself. Unchanged; no third target.
- **D2** — "the coordinator owns the durable-session contract; the helper preserves it."
  **Amended by D7.**
- **D4** — on a version mismatch the launcher may kill the old coordinator and lose its
  sessions, saying so out loud. Unchanged **for the coordinator process**; D3 makes its
  consequence false for sessions, which is the point of this document.
- **D8** — "one active client per session, and the loser is told"; `ws.go:64` is a single
  subscriber slot and `session.displaced` is a shipped contract (`nocx-oevq4`). **Not
  repealed — scoped** (D8 below): one active client per coordinator, N coordinators per
  session.
- **§8 B** — this epic (`nocx-k6p18`), which this design specifies.
- **§9** — "real multi-window ownership and read-only observers" were out of scope. This
  design brings the multi-coordinator half in, and leaves read-only observers out.

**The nocxify delivery-modes design** (`.internal/specs/2026-08-05-nocxify-delivery-modes-design.md`):
§3.4 named `relay` as the Tier-B carrier and deferred it "so the seam it lands in is decided
now rather than forked into later" — this is that seam. §3.5's three axes are kept whole;
_which carrier delivered integration_ is the observed-delivery axis's answer, as
`DeliversScripts`'s comment anticipated. §5.2's additive rule is kept.

**The remote-helper design** (`.internal/specs/2026-08-13-remote-helper-design.md`): D7's
install layout, D15's reserved `session` name — `host.Register` panics on `session` and
**only** on it (`host.go:85`); `files` and `ports` are unregistered names answering
`ErrCodeUnknownService` — D21's content hash, D25's prune and uninstall. **D4 below replaces
prune's rule; D14 keeps uninstall whole-tree and adds to it.**

**`nocx-isoph` (closed) and `nocx-kc2l6` (open)** — workspaces are backend objects and were
shipped as such; the owner re-decided on 2026-08-16 that tabs are too, with blocks hanging
off the tab. D5 puts the workspace's _namespace_ half on the execution host and leaves
every visible property with the coordinator.

## 3. Decisions

### D1 — The promise, split into what binds now and what waits

**D1a, binding.** Once an execution host acknowledges creation of a session, no nocx update
action — installation, activation, coordinator replacement, compatibility negotiation or
pruning — may terminate or signal its process, close its PTY, delete its live generation, or
**destroy the information and rendezvous a fresh compatible coordinator needs to reattach**.
The invariant ends only on natural process exit, explicit session close, user-confirmed
uninstall, host failure, or an explicitly authorised security revocation.

Two consequences, stated so they are not discovered:

- An update incompatible with a **non-core service** refuses that service and does not touch
  the survival ABI or the session (D3).
- A **security** update does not silently override D1a. Terminating a vulnerable generation
  is an explicit, user-authorised revocation that lists the live work first, exactly as
  uninstall does (D14).

Note the wording: _destroy the ability to reattach_, not _make unattachable_. Sequential
coordinator replacement necessarily has a window with no coordinator; that temporary refusal
is not destruction.

**D1b, blocked.** Whether a program keeps making _progress_ through an arbitrarily long
coordinator absence is **not decided here and not promised**. The ring is bounded and AD-10
requires losslessness, so at the bound the source throttles: the process is alive and
reattachable but not advancing. `nocx-8mllr` is closed as **superseded, not decided** — its
close reason moves the question to `nocx-22k1c`, which is open, and records that "nocx is
lossless TODAY... unacked bytes are never discarded".

**The `ssh` + `tmux` comparison is deliberately absent from this document.** tmux is
lossy-continue; AD-10 is lossless-block. Invoking it would paper over exactly the difference
`nocx-22k1c` exists to decide.

### D2 — The execution host: fat infrastructure, thin product

The helper owns, on the machine where the user's work runs:

- process creation and any policy that depends on being the process's ancestor — including
  the future Landlock/Seatbelt sandbox, which only the spawner can apply;
- PTY master lifetime; resize, signal delivery, foreground-group questions, exit status;
- execution-domain operations: login-shell resolution, native port enumeration, git,
  filesystem;
- the replay ring and the transport state detach/reattach needs: session identity, 64-bit
  offsets, bounded buffering, per-subscriber cursors, flow control, raw forwarding;
- a **workspace namespace** over its own sessions (D5);
- an **opaque durable record store** per workspace: it writes bytes and returns them on
  request, and knows nothing about what they mean (D11).

The helper does **not** own:

- any interpretation of the byte stream — no VT, no OSC, no block boundaries;
- blocks, the ledger's meaning, retention, the vault, `content.db`, the assistant, UI state;
- **policy decisions.** The coordinator decides which sandbox a tab gets, which mode a
  destination is in, and what a resize should be; the helper applies them.

**Rejected: a helper that emits command-block boundaries.** Under ADR-0019 those facts are
frontend-derived; a helper deriving them would have to read the stream's meaning, which
AD-6 forbids and whose two carve-outs are both coordinator-side. It would also make
helper/coordinator lockstep real, which D3 cannot survive.

**This amends AD-5**: on a host where the helper spawns the shell, Tier B replaces the
_delivery_ of the shell hooks — no rc-file editing, no bundle upload — and does not replace
their role as the source of prompt and block metadata.

### D3 — A small frozen core; everything else versions freely

**The survival ABI is frozen for the lifetime of the product.** It is deliberately small
enough that freezing it is a promise rather than a hope:

`attach`, `detach`, `write`, `data` (raw, session-keyed, offset-carrying), `ack`, `resize`,
`signal`, `exit`, `close`, `sessions` (enumerate a workspace's sessions).

`spawn` is **not** in it, deliberately: an old generation never needs to start new work.
New starts go to the current generation; an old one only continues sessions it already owns
and accepts attaches to them. Calling it a _survival and reattachment ABI_ rather than "the
session-host core" is the honest name.

**Everything else versions freely**: `git`, `files`, `ports`, sandbox policy shape,
capability reporting, the record store's operations. A coordinator that cannot speak an old
helper's `git` makes the git panel say why (D13); the session is untouched.

This is the only one of three available compatibility answers that keeps D1a under
AGENTS.md's "no backward-compatibility shims". The other two are recorded so they are not
re-proposed: a payload-blind router fronting versioned helpers needs a third process
(rejected in D6), and "break the contract, kill incompatible sessions" is D1a negated.
**Keeping several old protocol clients inside one coordinator is a shim under another name
and is refused.** Freezing one path forever is not a shim — but it _is_ a compatibility
obligation, and this document says so rather than implying compatibility went away.

**What must be frozen is more than ten verbs.** The ABI includes: framing and maximum
sizes; the core handshake version; session and generation identity; raw data routing with
**64-bit** offsets; attach ownership and lease semantics; ordering, reset and gap semantics;
error and refusal envelopes; authentication; exit/close race semantics; and service-version
negotiation for everything outside the core.

**Two corrections to an earlier draft of this section.** Type byte 8 is numerically free
(`frame.go:28`) and `seq`/`ack` were reserved in as many words "so a later PTY-owning
service can resume without a wire break" (`frame.go:15`) — but adding a type **is** a
vocabulary change, because `valid()` is a closed set and rejects 8 as garbage today. And
those header fields **cannot carry this contract**: `EncodeFrame(t FrameType, seq, ack
uint32, …)` is 32-bit and connection-wide, while `ring.base/acked/recorded` are `uint64`
and there is no session id in the header. A connection-wide 32-bit counter cannot express
per-session monotonic byte offsets and wraps at 4 GiB. **The raw payload therefore carries
session id and a 64-bit offset itself**; the header's `seq`/`ack` remain connection-level
sequencing and are not the AD-9 cursor. Since v1 has no live PTY sessions, the baseline is
bumped now and the freeze begins after.

### D4 — Generations coexist, and held generations are never pruned

`~/.nocx/helper/<version>-<goos>-<goarch>-<hash>/` is content-addressed: a new version
installs into a **new** directory and does not touch the old. That is generational storage,
already shipping.

Prune's rule changes from "not current" to:

> Remove an install directory that is neither `keep` nor **held** by a live generation.

**There is no cap on held generations.** An earlier draft carried the bundle publisher's
"at most two generations" bound; four updates across four long jobs produce three held old
generations, and a cap would have to break D1a to honour itself. Storage is bounded by
**live sessions**, not by a count. Disk growth is reported (D13), never resolved by deleting
held code.

**Two corrections about the existing install, so later work does not rely on them.** The
directory is **not** atomically published: `install()` calls `mkdirAll` on the final
directory, writes a temp file inside it, renames the _file_, then writes the marker —
completeness is marker-gated only. And "immutable" is a convention: `dirMode` and
`binaryMode` are `0700`, owner-writable.

**Prune is also not the only thing preventing coexistence.** The larger blocker is that the
helper today exits when its SSH lane reaches EOF (`host.go:154` → `client.go:188` →
`launch.go:211`). Both are fixed, and the second is the lifecycle document's subject.

### D5 — A workspace is a namespace on the execution host; attach is `(host, workspace)`

Attaching names a **workspace**, never "the host". Two people on one server pick different
workspaces and never see each other's sessions — not because anything is hidden, but because
they were never in one namespace. One person on two machines picks the same workspace and
sees the same sessions. A host opened without a name lands in that account's default
workspace, exactly as `nocx-isoph` already does locally.

The split:

- **the execution host owns** the workspace's name, its membership (which sessions are in
  it), their identities and their liveness — a namespace over sessions it already owns;
- **the coordinator owns** everything a person sees about it: title, colour, pinning, order,
  tabs, blocks, ledger.

So `sessions` in the frozen core is workspace-scoped, and "list the panes on this host" is
not an operation that exists. This is what makes a listing operation fit D2: membership is
infrastructure, decoration is product.

**There is one mode, not two.** `(host, workspace)` is the address; a missing workspace name
resolves to a default rather than selecting a different mode.

### D6 — No router: the durable handle addresses its generation

A third stable process is avoidable, and the condition is exact. All of these must hold:

1. the durable session handle **includes generation identity**;
2. the generation's endpoint is **deterministically derived** from that identity;
3. the mapping is **committed durably before** the session-open acknowledgement;
4. a fresh coordinator recovers the whole handle **without consulting a live predecessor**;
5. an old generation never needs to interpret new product semantics to accept an attach —
   which is what D3's frozen ABI buys.

Condition 3 is not decoration: a mapping held only in coordinator memory recreates the
router trigger at the next restart. The sharpened trigger, recorded so it is recognised
rather than rediscovered:

> Add a router only when session ownership or endpoint selection requires **lookup** rather
> than deterministic **derivation**.

D6's proof obligation is therefore durable direct addressing, not the mere absence of a
router.

**This is the AD-7 amendment**, made explicitly rather than by redefining "server" in prose:
the execution host mints session identity, the durable handle names generation and session,
and the coordinator's registry becomes authoritative over _panes and tabs_, not over session
existence.

**And the endpoint is derived, not spelled.** `sockaddr_un.sun_path` is ~108 bytes on Linux
and ~104 on macOS; `$HOME` + `<version>-<goos>-<goarch>-<64-hex>` exceeds that on an
ordinary machine. The runtime endpoint is a short encoding derived from the generation hash
under a private runtime directory; the full identity participates in the handshake instead.

### D7 — The whole read pipeline moves, not a data structure

**This amends D2 of the nocx-server design.** That decision put the durable-session contract
in the coordinator, citing the delay-fuse warning. The concern is right and the placement is
not, for a physical reason: **only the process holding the fd can say what was produced while
nobody was listening.** With the coordinator absent it cannot buffer; either the helper
buffers or bytes are lost and D1a is false.

`pumpToRing` is today the junction of **three connection-independent consumers**, and their
co-location is itself the design: the enrolled VT grid (`ws.go:2931`, and the code says the
grid is fed "on the backend's own read path, and deliberately not on the subscriber path"),
the durable recorder (`:2936`, "started HERE, beside the pump and for the same reason"), and
the replay ring (`:2945`). Moving only the ring orphans the argument for the other two.

The topology, local and remote alike:

```
PTY master
  → helper: sole reader, no interpretation
  → helper-owned ring (per session)
  → raw session-keyed, offset-carrying frames
  → coordinator fan-out
       ├─ enrolled VT grid            (AD-6 carve-out stays coordinator-side)
       ├─ recorder → content.db       → persisted(offset) back to the helper
       └─ WebSocket subscriber        → ack(offset) back to the helper
```

The **implementation does not fork**: `internal/transport/ring.go` is Go and so is the
helper, so both link one package. One implementation, instances in two places — the pattern
already in the tree, where `app.go:1039` uses `gitlocal.NewFactory()` directly and
`cmd/nocx-helper/main.go:41` wraps the same factory in `hostsvc`.

Cost, named so it is planned: the ring leaves `internal/transport` for a package the helper
can link without dragging the transport in.

Five obligations this topology creates, each owed an assertion:

1. Enrolment requiring byte-zero observation is committed **before spawn**, or the grid is
   initialised by replay from offset zero.
2. Coordinator replacement must not double-consume into grid or recorder.
3. The recorder's cursor means **durable commit**, not receipt.
4. Frontend ack and recorder-persisted cursor keep their present, independent reclamation
   rules — `ring.recorded`'s own comment: "IT IS NOT AN ACKNOWLEDGEMENT, and the two must
   never stand in for each other."
5. A reset caused by ring reclamation has a defined effect on the backend grid as well as
   on the renderer.

**AD-10 binds the helper unchanged**: with nothing attached and the ring full, the source
throttles. Backpressure is acceptable; loss is not. Whether that is the right product
answer is D1b's question and `nocx-22k1c`'s to answer.

### D8 — N coordinators per session; D8 of the nocx-server design is scoped, not repealed

A desktop and a laptop are not two clients of one coordinator. They are **two
coordinators**, each with its own window, vault, `content.db` and settings, attached to one
generation.

So the multiplicity lives one floor down and the shipped decision survives intact:

- **one active client per coordinator** — `ws.go:64`, `session.displaced`, unchanged;
- **N coordinators per session** — new, at the helper boundary.

Displacement keeps meaning what it meant: two windows on one machine. Two machines are two
subscriptions.

**Cursors go from two to N, and the rule is the one already implemented.** The ring today
holds `acked` and `recorded` as independent cursors and frees a byte only when both have
passed it. N is the same rule, not a new concept: a byte is reclaimable when **every live
subscriber cursor** has passed it.

**And a sleeping laptop must not stop a desktop.** A subscriber that stops advancing would
fill the ring and throttle the session for everyone. A subscription is therefore a **lease
with a bounded renewal**, and an expired subscriber's cursor is dropped from the minimum.
This is a clock, and it is a legitimate one: the renewal interval is short and known, unlike
a session's lifetime — which is precisely why the publisher's timeout could not serve D9 and
can serve here.

**Resize follows the focused viewer**, per pane — herdr's policy, and better than tmux's
smallest-attached: a pane sizes to whichever viewer is currently looking at it. The
coordinator knows who is focused, so it is a coordinator decision the helper applies (D2).

**Concurrent input is allowed and interleaves**, as it does in tmux. Read-only observers
remain out of scope, as the nocx-server design §9 left them.

### D9 — Liveness is a kernel fact, and deletion holds it

The bundle publisher's lock **cannot** be extended here, and the earlier draft that said it
could was reusing a name rather than a solution. Verified: `acquireLock` probes five times
over 1.55 s and then **breaks the lock unconditionally** (`publisher.go:1164`). That is
correct for what it guards — a publish is bounded, so elapsed time is a valid liveness proxy,
and breaking it costs duplicate work, never lost bytes. A generation hold is unbounded: 1.55
seconds would declare a healthy three-hour job stale. The nonce identifies which attempt
created the directory; it proves nothing about life.

An unbounded hold needs a **kernel-lifetime primitive** — released by the kernel on process
death, trusting neither PID nor clock. And because SFTP cannot test a remote advisory lock,
determination runs on the host while deletion stays with the coordinator:

1. The generation daemon holds a live lock for its lifetime.
2. `nocx-helper probe-prunable <generation>` attempts the same lock **exclusively,
   non-blocking**.
3. Failure ⇒ held ⇒ report live and exit.
4. Success ⇒ unheld ⇒ report a nonce over the framed exec lane and **retain the lock**.
5. The coordinator deletes over SFTP **while that probe process holds it**.
6. The coordinator signals completion; the probe releases and exits.
7. Carrier loss before completion releases the lock **and stops the deletion**.

Step 5 is the whole point: it holds exclusion across the window between deciding and
deleting. **A socket probe cannot do this** — a successful connect is good positive evidence
of life, but a failed connect is not safe negative evidence of death (accept backlog,
descriptor exhaustion, a broken listener loop on a daemon that still owns PTYs, a forwarding
failure), and it cannot hold its conclusion until the later SFTP delete.

So the two mechanisms answer different questions and neither replaces the other:

| mechanism           | question                                                            |
| ------------------- | ------------------------------------------------------------------- |
| rendezvous socket   | can I communicate with this generation? (discovery, attach, health) |
| lifetime/prune lock | can I prove deletion is mutually exclusive with a live generation?  |

**Invariant this requires**: while a prune lease is held, a retired generation may not be
resurrected. Only the current generation may start a daemon or a session; an old one may
only continue live instances and accept attaches to sessions it already owns.

### D10 — The coordinator deletes; the helper only tells the truth about itself

Deletion stays where installation already is: the coordinator, over the SFTP lease it holds
anyway, at connect time.

**Rejected: a new generation cleaning up an old one.** It would grant a helper write
authority over a sibling's directory, ask the process with the least context to judge
another's liveness, and still not cover the case pruning exists for — a generation killed
without running its own cleanup. The helper's only contribution is D9's lock and probe: a
_read_ executed on the host, never a delete.

**And the severity of getting this wrong survives a correction to its mechanism.** Deleting
a running executable's directory does **not** kill the process — the inode outlives the
unlink. It destroys the generation's durable identity and rendezvous, permanently orphaning
its live PTYs: the program and its descendants go on consuming CPU, memory and external
resources while nocx can no longer read output, send input, resize, signal or deliberately
stop it, and reinstalling identical bytes does not reconstruct the lost attachment state.
That violates D1a more directly than termination would, because nocx loses both control and
recovery.

### D11 — The ledger stays with the coordinator; the store moves to the host

"The store lives on the host" and "the helper owns the ledger" are different claims, and
only the first is made.

- The helper offers a **dumb durable record store scoped to a workspace**: opaque encrypted
  blobs with an ordering key. No schema, no interpretation, no key material. It writes bytes
  and returns them on request.
- The coordinator remains the **only writer** and the only thing that understands an entry.

ADR-0019's title then applies literally: the authoritative ledger is the workspace's store
on the host, and each machine's local `content.db` becomes a **disposable projection**.

**Where this extends rather than applies.** ADR-0019 was written about _authorship_ — one
ledger for human and agent work — not about _locality_. The shape fits; treating a
host-resident store as the authoritative one is a new decision, and it is recorded as one.

**Why the ledger itself may not move.** Under ADR-0019 block facts are frontend-derived and
cross the control plane; the helper never sees the renderer, so owning them would mean
deriving them from the stream — AD-6, whose two carve-outs are both coordinator-side. It
would need the vault, because entries carry command text and ADR-0021 puts secrets there,
so the key would live on the far host. It would put a product-evolving schema on the helper
wire, which is D3 negated. And it is a large share of the measured 40.2 MB / 2.8 MB gap
between coordinator and helper.

**Open and separable: the key.** Both machines must read one workspace store, so both need
its key. That is key sync between one person's own machines — real work, bounded, and
deliberately **not** in this deliverable. Until it exists, a workspace store is readable by
the machines that hold its key, and the product says which. Named follow-up; it blocks
nothing here, because per-machine projections degrade honestly on their own.

### D12 — The wire stays what it is, and `contracts/` covers it

The framing stays: JSON payloads for control, raw frames for data. No JSON appears on the
hot path — data is raw bytes, and the frozen core's verbs happen at attach/detach frequency.

**gRPC/protobuf is rejected**: a heavy dependency in a binary whose 2.8 MB just decided D5's
architecture; HTTP/2 does not fit an stdin/stdout exec lane; and it would be a second
control-plane vocabulary in one product, against AD-8 and against AD-1's existing choice.

**OpenRPC is rejected as redundant, not as bad.** It describes JSON-RPC; `contracts/`
already does that job here — 382 schemas, generated renderer types, Go validation, and
over-the-wire conformance tests — and it **already covers helper-served methods**, since
`git.*` lives there. A second description format for one surface is AD-8 inverted.

**The limitation is stated rather than papered over: JSON Schema describes shapes, not
sequences.** The hard part of the frozen ABI is ordering and state — attach → data → ack,
monotonic offsets, reset, single-writer, lease expiry. Neither JSON Schema nor OpenRPC
expresses that. The ABI's contract is therefore **schema plus assertions**, and the
load-bearing half is the assertions.

### D13 — What the product says, and what it stays quiet about

**A tab is not marked for running on an older generation.** After an update most remote tabs
would sprout a badge meaning "everything is fine", which is how people learn to stop reading
badges. AGENTS.md requires a _degrade_ to be visible; an older generation is not one.

1. **Nothing degraded** — no tab marker. The facts live where a person asks what nocx put on
   a host: `shell.footprint.status`, already read-only, already never connecting, already
   honest about "last seen". It gains generation rows, and disk used by held generations
   (D4). The kit component is `FactList`, whose `note` field is documented as "the honest
   half. A value the product cannot fully vouch for" — exactly a fact observed at last
   contact. **It must be labelled and tested as last-observed inventory**, never as current
   truth.
2. **A helper-served feature is genuinely unavailable** because that service's wire changed.
   The affected surface explains itself **at the point of use** — the git panel says this
   host is on the previous generation and returns when the session ends. Not a generic badge
   whose meaning must be hunted. This is the defence against the `vault.status` failure: a
   surface offering what it can no longer deliver.
3. **The session is durable and currently detached** — this deserves a tab marker, because
   it is new information about the session, and `nocx-k6p18` names the consequence: "'the
   channel is closed' stops meaning 'no helper is running'". `Badge`, `tone="info"`, with
   `title` carrying the reason.

**The script bundle.** §5.2's additive rule holds: allowing the binary never withholds the
scripts. What changes is the _carrier_ — where the helper spawns the shell, integration
arrives at spawn instead of through a published bundle, and which carrier ran is the
observed-delivery axis's answer. The bundle is not additionally published for a
helper-spawned session; it remains published for every other session on that host.

### D14 — Uninstall is whole, lists what it kills, and revokes consent first

All-or-nothing over `~/.nocx/helper`: those directories are content-addressed and nobody
hand-edits them, so partial removal would be an invented concept. This is the helper tree
only — the **script bundle** keeps its opposite and deliberate rule (manifest-owned
unmodified files only; a user-modified file is a reported conflict and stays).

1. **The confirmation lists the live work it will end**, and `deploy.Uninstall`'s
   precondition — no helper running out of a directory being deleted — stops being free:
   satisfying it _is_ ending the user's work, so it is raised into the product as a consent
   moment rather than left as a caller's obligation. `Dialog` plus a `FactList` row per
   session.
2. **The list is bound to a snapshot token, not merely to one connection.** Same-connection
   listing does not remove the TOCTOU: sessions can start or exit between listing and
   confirmation. New starts are quiesced and the confirmation names the epoch it saw.
3. **Consent is revoked first, then files are removed.** `RelayConsent` returns to `unknown`
   before deletion, because the failure modes are asymmetric: revoking first and failing to
   delete leaves inert files, while deleting first and failing to revoke **silently
   reinstalls on the next connection** — the user watching their removal evaporate with
   nothing saying why. ADR-0034 is untouched: consent stays keyed to the machine; only the
   stored answer is cleared, so the next connection asks.

### D15 — One shape locally and remotely; the helper reads everywhere

The local case is **not deferred**, and the argument that settles it is AD-8's, not
convenience: if the ring were helper-side remotely and coordinator-side locally, `pumpToRing`
would exist in two topologies and reset, replay, backpressure and the cursor rules would have
**two homes**. That is D2's delay fuse arriving from the other direction.

Consequences, each accepted with its cost:

- **The helper is the sole reader everywhere; the coordinator fans out everywhere.** This
  _rejects_ local fd passing, which was the earlier preference. Handing the master fd to the
  coordinator would make the coordinator the local reader and the helper the remote one —
  reintroducing the split this decision exists to remove. Local sessions pay one hop:
  window → coordinator → helper. **AD-1 does not license it**; it is accepted because
  uniformity is worth more than the hop, and the hop is a unix-socket copy — the same one the
  remote path pays minus the network. It is measured, not assumed (§6).
- **D1a is delivered locally too.** The nocx-server design's D4 may kill an old coordinator
  and lose its sessions; with the local helper owning the PTY that stops being necessary.
- **Local install reuses `deploy` verbatim** with a local FS adapter, into the same
  `~/.nocx/helper/<version>-<goos>-<goarch>-<hash>/`. **Not** as a sibling binary in the
  bundle beside `nocx-server`: updating the bundle replaces that binary in place, which is
  exactly what generational coexistence forbids. So "one scheme everywhere" is literal,
  including D4's prune rule and D9's lock, unchanged.
- **`make build` gains one native helper build.** Locally the host's own target is needed,
  not the cross-compiled matrix; the 2×2 matrix stays a prerequisite of the release build
  for remote upload only. This is also what stops a dev stand from having no local helper.

## 4. What the user sees

**An update.** The coordinator installs vN into a new directory; vN−1 is untouched and its
process runs. New tabs open on vN; existing tabs reattach to vN−1, because their handle names
their generation. The conversation with vN−1 runs over the frozen ABI. If a non-core service
changed its wire, that panel — on that tab — says why. When vN−1's last session ends its
daemon exits, its lock releases, and the next connect prunes it. Nothing is interrupted; the
most a person notices is a panel explaining its own absence.

**Two machines.** Desktop and laptop attach to the same `(host, workspace)`. Both see the
same sessions and the same bytes; each pane sizes to whichever viewer is focused on it; both
can type. Each machine's history is its own projection of the workspace's authoritative
store. Closing the laptop's lid expires its lease and does not throttle the desktop.

**Two people.** Two workspaces on one host, chosen by name. Neither sees the other's
sessions, because they were never in one namespace.

## 5. Assertions

Written as assertions rather than prose (AGENTS.md testing rule 4), and the ones that matter
are written from this spec by someone who did not implement it.

**Continuity (D1a, D4)**

1. A process spawned by vN−1 is still running — **proven by the same remote PID and process
   identity, from a fresh coordinator** — after replacement by a coordinator carrying vN, and
   it produced **more than `RingCapacity`** while detached.
2. A session opened after that replacement is served by vN — **asserted by distinct helper
   PID and content hash**, never by a version label.
3. Installing vN removes no file inside vN−1's directory, **through the production
   install-and-prune path** rather than an install with pruning disabled.
4. With vN−1 held, prune removes nothing; after its hold releases, prune removes it —
   **driven through a real generation daemon**, not a synthetic hold.
5. A generation killed without cleanup is pruned on a later connect, with staleness decided
   by the D9 lock — no remote PID read, no wall clock compared.
6. With **three** held old generations plus current, prune removes none of them and the
   footprint is reported.
7. The frozen ABI is exercised by a coordinator against **an archived released helper
   binary**, not against the same source with a changed version constant.

**The frozen ABI (D3)**

8. A non-core service whose wire changed answers a version refusal naming the service, **over
   the real wire**, while the same session continues reading and writing.
9. A raw data frame round-trips a payload containing invalid UTF-8 and bytes resembling a
   frame header, across split reads and adjacent frames, with multiple sessions multiplexed
   and offsets above 2³², and the decoder's gap callback reports nothing.

**Ring, cursors and backpressure (D7, D8)**

10. Both production composition roots construct the exported concrete ring type, proven
    reachable by `deadcode -whylive`; and **one shared trace suite drives the real
    coordinator and helper adapters** through write, attach, ack, persisted-cursor advance,
    detach, pressure, replay, reset and close, observing identical offsets, output and
    blocking. A boundary ratchet forbids replay cursor and reset arithmetic outside that
    package. (`-whylive` proves production reachability only; it cannot prove semantic
    singularity, and this assertion no longer claims it does.)
11. Producing more than capacity with nothing attached blocks the producer; draining and
    acking then yields the exact length, hash and order.
12. A reattach after coordinator replacement replays exactly from the last acked offset or
    states its gap, and **the frontend is observed to clear and resync on reset** — not
    merely an internal flag.
13. With two subscribers, a byte is reclaimed only after both cursors pass it; when one
    subscriber's lease expires its cursor is dropped and the other is not throttled.
14. Grid enrolment sees byte zero — or is initialised by replay from zero — and coordinator
    replacement double-consumes into neither grid nor recorder.

**Failure paths (AGENTS.md rule 3)**

15. Install fails at each enumerated write boundary in turn; vN−1 keeps serving, and the next
    connect converges through the automatic prune and restart path with no manual cleanup.
16. The lock cannot be taken because the directory is read-only: the product refuses visibly,
    names the reason, **spawns no child**, and deletes no generation.
17. The carrier drops mid-session: **a fresh coordinator reattaches and proves the same PID
    and exact stream continuation** — process survival alone is not the assertion.
18. **And the paired positive**: on an ordinary host with an ordinary home, using the shipped
    coordinator and helper binaries over a real carrier, a generation is installed, held,
    served, reattached and pruned end to end.

**Product surfaces (D5, D13, D14)**

19. Two workspaces on one host: neither `sessions` call returns the other's sessions, and no
    operation exists that lists a host's sessions irrespective of workspace.
20. After an update with an old generation serving, no tab acquires a badge, and
    `shell.footprint.status` lists both generations **without connecting** and is labelled
    last-observed.
21. A detached durable session's tab carries the info badge, and it clears on reattach **by a
    fresh coordinator**.
22. Uninstall's confirmation lists every session it will end, bound to a snapshot token, and
    a session started after the snapshot invalidates the confirmation rather than being
    silently killed; declining ends and removes nothing.
23. Consent is revoked before any file is deleted: a failure injected after revocation leaves
    files and no grant, and the next connection asks rather than reinstalling.

## 6. Measurements this design rests on

- **40.2 MB vs 2.8 MB** — `nocx-server` and `nocx-helper`, both `CGO_ENABLED=0 -ldflags
"-s -w"`, measured 2026-08-31 on linux/amd64. This is why a full server per node is
  rejected in favour of an execution host: the four-target embed would be ~160 MB before
  compression instead of ~11 MB.
- **The local hop, unmeasured and owed.** D15 accepts one unix-socket copy per keystroke and
  per output byte. Before the local execution host ships, this is measured at interactive
  latency and at sustained output, and the number is recorded here. AD-1 does not settle it.

## 7. Deliberately out of scope

- **Key sync between a person's machines** (D11). Named, separable, blocking nothing.
- **A router process.** D6 records the single trigger.
- **Read-only observers**, as the nocx-server design §9 left them.
- **Autonomous work with no coordinator attached.** `ws_agent.go:756` stands: a run
  terminalizes on disconnect. The helper keeps the process alive; it does not advance the
  agent.
- **Sandbox implementation.** D2 places it helper-side. Fact worth recording: no sandbox
  package exists in this tree — `nocx-y46q` is closed but `grep -l 'landlock|seatbelt|
sandbox-exec' --include=*.go` is empty and ADR-0035 is now about AppImage. Greenfield, and
  it lands helper-side from the start.
- **The `files` and `ports` services.**
- **Windows remote hosts.**
- **Graceful coordinator handover (A2).** Replacement stays sequential — the old coordinator
  closes the encrypted store and exits before the new one opens it. This design _avoids_
  ADR-0043 rather than solving multi-process encrypted SQLite.

## 8. Open questions

1. **`nocx-22k1c`'s question, now reachable in a second place**: what the ring does when the
   recorder is unavailable or slower than the source. It is D1b's blocker. Unchanged by this
   document except that a helper-side ring makes it reachable during coordinator absence too.
2. **The local hop's cost** (§6), owed as a number before the local execution host ships.

## 9. What must exist before this design is accepted

The durable lifecycle protocol, as its own document. This is not deferred implementation
detail: several plausible implementations satisfy every word above and make D1a **false** —
most obviously a daemon treating bridge EOF as shutdown, which is what the code does today.
It must define daemonization and parent-death behaviour; deterministic endpoint derivation;
atomic socket establishment and stale-path recovery; peer authentication and attach
authorisation; durable generation-bearing handles; single-writer/reader ownership across N
subscribers; the SSH streamlocal and exec-bridge behaviour; reconnect after carrier loss and
after coordinator replacement; the lifetime/prune lock; and shutdown rules distinguishing
bridge EOF, last detach, process exit, explicit close and uninstall.
