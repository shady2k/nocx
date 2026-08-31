# The authenticated lifecycle protocol

- **Status:** Implemented (kernel model) / normative for adapters and consumers
- **Date:** 2026-08-08
- **Implements:** [ADR-0024](decisions/0024-authenticated-shell-integration-channel.md),
  decisions 2, 3, 5, 6, 7 and 8. Read the ADR first; this document is the wire
  contract it leaves open, plus the state model the kernel enforces.
- **Epic / bead:** `nocx-u7uh` / `nocx-u7uh.2` (the protocol kernel)
- **Reference implementation:** `internal/lifecycle` (Go, pure model — no
  transport, no shell code). The kernel is the normative state machine; a
  conformance test that disagrees with this document is a bug in one of them.

This document specifies the authenticated channel over which a shell reports its
command lifecycle, and the kernel model that consumes it. A shell adapter author
and a frontend author can both work from this document alone.

## 1. Vocabulary

| Term           | Meaning                                                                                                                                                                                                                                                                                                            |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Transport**  | The channel that carries envelopes. Local: a descriptor handed over at spawn. Remote: a loopback TCP port obtained from the remote sshd via `TunnelConn.Listen` (`internal/ssh/ssh_tunnel.go`), connected by the shell's bootstrap. One transport may carry several domains; several transports may feed one lane. |
| **Lane**       | One input-routing lane (one terminal tab). At most one **active** domain per lane. A lane holds a stack of domains; the top of the stack is the active one.                                                                                                                                                        |
| **Domain**     | One authenticated shell or helper instance. Logical — never an alias for a transport. Carries an id, an epoch and an optional parent.                                                                                                                                                                              |
| **Epoch**      | The generation of a domain instance. Monotonic per kernel instance, assigned at creation, never reused, never resumed: a new establishment is a new domain with a new epoch.                                                                                                                                       |
| **Capability** | The per-epoch authenticator: at least 256 random bits, minted by the kernel, substituted into the integration script text, never passed as an environment variable, never derived from the transport.                                                                                                              |
| **Attempt**    | One command execution. Belongs to exactly one domain; cannot cross an activation boundary.                                                                                                                                                                                                                         |
| **Lifecycle**  | The per-lane authority axis: `Native                                                                                                                                                                                                                                                                               | PromptReady(domain) | Running(attempt) | Desynchronized(domain) | Lost` (ADR-0024 decision 6). |

## 2. The envelope

Every event travels in an envelope. **Every** envelope carries the full addressing
tuple — protocol version, lane id, domain id, epoch, monotonic sequence, and the
bearer capability. No API anywhere obtains lane, domain or epoch from a singleton;
they are addressed explicitly in every message. This is the property that keeps
the remote helper a third adapter instead of a protocol rewrite.

```
+--------+--------+--------+--------+------------------+
| length |  v:1   |  lane  |  dom   |  epoch (u64)     |
+--------+--------+--------+--------+------------------+
|  seq (u64)      |  cap (32 bytes)   |  event payload  |
+-----------------+-------------------+-----------------+
```

Wire encoding: **JSON** (UTF-8), length-delimited by a 4-byte big-endian length
prefix preceding the JSON bytes. The envelope fields:

| Field      | JSON    | Type                   | Rule                                                                                |
| ---------- | ------- | ---------------------- | ----------------------------------------------------------------------------------- |
| Version    | `v`     | integer                | `1` today. Anything else is rejected before any state is consulted.                 |
| Lane       | `lane`  | string                 | The lane the event addresses. Must equal the addressed domain's lane.               |
| Domain     | `dom`   | string                 | The domain instance. Must exist and be bound to the transport the frame arrived on. |
| Epoch      | `epoch` | integer                | Must equal the domain's live epoch.                                                 |
| Sequence   | `seq`   | integer                | Strictly increasing per domain within its epoch. See §11.                           |
| Capability | `cap`   | 64 lowercase hex chars | The bearer. Authenticates the frame. See §4.                                        |
| Event      | `evt`   | string                 | The event kind, §6.                                                                 |

Outbound envelopes (kernel → shell: `accept`, `refresh_request`) use the same
envelope. The bearer authenticates both directions; the shell's copy of the
capability is what its bootstrap holds.

## 3. Event kinds

| Kind                 | Direction       | Payload                                                                  | Meaning                                                                            |
| -------------------- | --------------- | ------------------------------------------------------------------------ | ---------------------------------------------------------------------------------- |
| `hello`              | shell → kernel  | `shell` (kind), `max_frame` (optional)                                   | First frame of a connection. Establishes the domain (§5).                          |
| `accept`             | kernel → shell  | —                                                                        | The domain is live; the shell may suppress its prompt and emit lifecycle events.   |
| `start`              | shell → kernel  | `attempt` (optional), `command`                                          | A command began. §7.                                                               |
| `complete`           | shell → kernel  | `attempt`, `exit_code` (optional), `fence`                               | A command ended. §8.                                                               |
| `prompt_ready`       | shell → kernel  | —                                                                        | The shell is at a prompt; the editor may own keys.                                 |
| `refresh_request`    | kernel → shell  | `request`                                                                | nocx demands an authenticated state snapshot. §10.                                 |
| `snapshot`           | shell → kernel  | `request`, `shell_state`, `active_attempt`, `last_completed`, `next_seq` | The shell's authoritative state. §10.                                              |
| `domain_established` | kernel → (fact) | —                                                                        | Published when the handshake completes; the frontend keys enhanced mode on it.     |
| `domain_suspended`   | shell → kernel  | —                                                                        | The shell is entering a nested environment; its domain yields the lane.            |
| `domain_activated`   | shell → kernel  | —                                                                        | The shell is back; its domain reclaims the lane.                                   |
| `domain_closed`      | shell → kernel  | —                                                                        | The shell instance is ending.                                                      |
| `domain_request`     | shell → kernel  | `request`, `env`, `host`/`user`/`port` (ssh)                             | The parent asks for a child domain for a nested environment it is entering. §9.    |
| `domain_grant`       | kernel → shell  | `request`, `domain`, `epoch`, `bootstrap`                                | The answer: the child's identity and the opaque bootstrap the parent executes. §9. |
| `agent_enrol`        | shell → kernel  | `request`, `agent`, `cols`, `rows`                                       | The pane is about to run an agent; keep its screen. §15.                           |
| `agent_enrolled`     | kernel → shell  | `request`, `agent`, `enrolled`, `reason`                                 | The verdict, and the reason when it is no. §15.                                    |
| `agent_withdraw`     | shell → kernel  | `request`                                                                | The agent has returned; the interval closes. §15.                                  |
| `agent_withdrawn`    | kernel → shell  | `request`                                                                | The close is acknowledged. §15.                                                    |

`accept`, `refresh_request`, `domain_grant`, `agent_enrolled` and
`agent_withdrawn` are kernel-originated; ingesting them from a shell is a
protocol violation. Everything else is shell-originated.

## 4. Authentication

**Property (ADR-0024 decision 2):** possession of the transport is not possession
of the domain. An inherited descriptor, a discovered listening address, or a mere
connection must never let a descendant or another local user publish an event.
Every envelope therefore carries the domain's per-epoch **bearer capability**, and
the kernel verifies it **before any domain or sequence state is consulted or
mutated** (decision 7). A frame with a wrong capability is rejected as if it never
arrived: no state read, no counter advance, and the failure counts toward the
handshake rate limit (§5).

**Why bearer, not MAC.** The threat model's transport reachability varies — a
descendant holds the local descriptor; any local user on the remote host can open
the forwarded port — but every party that can observe frames on the transport is
in the same trust class as one that could read a shared key out of the same
process or socket memory, so a MAC adds an assumption (a key the shell can compute
with, i.e. a helper binary) without adding a boundary. What a MAC would defend —
an attacker who can read frames but not the key — does not exist in the ADR's
stated threat model, and bearer is implementable by the shells we support today
(bash network redirection, no crypto). The capability is 256 random bits, per
epoch, never exported, never in the environment, and replay-safe within its epoch
via the sequence rule (§11); authority rotates with the epoch, and a new epoch is a
new capability.

The capability never enters a filesystem object where the installer can avoid it;
the bootstrap script is substituted at install time (`@CAP@`, the pattern
`internal/shellintegration` already uses for `@SID@`), and the kernel keeps it only
in memory on the domain record.

## 5. Establishment: the handshake

A listener existing is not a channel being live (decision 3). The sequence:

1. nocx establishes the transport (local: descriptor handed over at spawn;
   remote: `TunnelConn.Listen`). Refusal — `AllowTcpForwarding off`, or a bind
   outside `PermitListen` — ends here, in conventional mode.
2. nocx mints the domain (id, epoch, capability) and the shell's bootstrap embeds
   the capability.
3. The shell connects and sends `hello` (sequence 1, bearer = capability).
4. nocx validates version, domain, epoch, capability, lane legality and parent
   state, and answers `accept`.
5. **Only after `accept`** may the shell suppress its prompt or emit lifecycle
   events. The kernel enforces this: lifecycle events for a domain that is not
   `Established` are rejected. Enhanced mode is entered only after the frontend
   has the published `domain_established` fact.
6. Timeout (`hello_timeout`, 10 s) or any failure leaves the visible native
   prompt in place.

`accept`, `refresh_request`, `domain_grant`, `agent_enrolled` and
`agent_withdrawn` are kernel-originated; ingesting them from a shell is a
protocol violation. Everything else is shell-originated. **Three outbound kinds, one boundary:** the transport port
carries exactly three kinds of envelope — `accept`, `refresh_request` and
`domain_grant`, the replies the shell must see. `domain_established` (and
every other fact about domain and attempt state) is **not** a transport
envelope: it is a published fact for the frontend projection layer, derived
from the kernel's read model by the publication bead
(`nocx-u7uh.5`/`.13`). Adapters implement the port and see only the three
reply kinds; projection authors consume the read model. There is no fourth
path.

**The first authenticated connection claims the epoch.** A `hello` for an
already-`Established` domain with a matching capability is a _reconnect_: it is
accepted (a fresh `accept` is answered) and the sequence counter is **not** reset
(§11). A `hello` with a wrong capability for a live domain is a rejected
candidate: it can neither mutate nor preempt.

**Bounds.** Failed handshakes are rate-limited per lane: 8 failures per 30 s
window; beyond that, new establishment requests on that lane are refused until the
window drains. Connection count and handshake time are bounded at the transport
adapter (the same numbers the kernel enforces for frames: `hello` ≤ 1 KiB, frame ≤
256 KiB, handshake ≤ 10 s).

## 6. Frames and budgets

- **Framing:** 4-byte big-endian length prefix, then the JSON bytes. Length is
  the JSON byte count. Frames are size-bounded: **max_frame = 256 KiB**; `hello` is
  additionally bounded to **1 KiB**.
- **Desynchronization** (decision 7): a framing gap or corruption does not revoke
  the epoch — every ordinary program would be a one-write kill switch. The domain
  enters `Desynchronized` and the lane's lifecycle becomes `Desynchronized(domain)`.
- **Budgets, per desync episode, concrete:**
  - `scan_bytes` = **64 KiB** of garbage scanned by the adapter,
  - `scan_frames` = **128** garbage frames,
  - `scan_duration` = **30 s** since the episode began,
  - `max_desync_episodes` = **3** per domain lifetime.
- Exhausting any budget **revokes the domain**: it is closed, its open attempts
  become `unknown`, and the lane falls to `Native`. Availability against a
  descendant that continuously writes to the transport is not guaranteed and
  cannot be; integrity and safe recovery are (ADR-0024 decision 10).

The adapter scans forward for the next frame boundary within `scan_bytes`; every
garbage region it skips is reported to the kernel so the budget can be enforced in
one place. A validly-framed, authenticated envelope found by scanning is delivered
to the kernel, which quarantines it (rejects it, mutating nothing) while the
domain is `Desynchronized` — framing recovery is not state recovery.

## 7. Attempts: start

An attempt is open from submit or authenticated `start` until an authenticated
same-domain `complete` (decision 5). At most one attempt is open per domain at a
time.

**App-originated (submit).** The editor submit synchronously creates the attempt
— id, app-owned command text, cwd, host, start time — **before** the bytes that
could cause the shell's own `start` are written to the pty. The lane moves to
`Running(attempt)`.

**Shell-originated (start).** Both origins are legitimate, because an
authenticated `start` is exactly as attributable as an authenticated `complete`:

- `start` with an explicit `attempt` that matches the single pending app attempt
  → **attaches**; the attempt keeps its id, command text, cwd, host and secret
  representation. The shell's `command` field is ignored outright (privacy rule:
  the wire line may carry vault-resolved secrets while the app's text carries
  references).
- `start` without an `attempt` and a pending app attempt in the same domain →
  attaches to it.
- `start` with an explicit `attempt` that matches nothing → creates a
  shell-originated attempt with that id.
- `start` with an explicit `attempt` that **mismatches** a pending app attempt →
  rejected (a second top-level attempt over a pending one is never silent).
- `start` while an attempt already runs in the domain → rejected: a nested event
  or a protocol violation, never a second top-level attempt.

A `start` is legal only from lane lifecycle `PromptReady(domain)` with the domain
active and `Established`.

## 8. Attempts: completion, the fence

`complete` carries an **optional** attempt id, an optional exit code, and the
**fence**: 32
random bytes (64 hex chars) the shell generated when the command finished and also
writes to the pty **after** the command's output. The fence is a rendezvous for
render ordering — the visual freeze waits for both the authenticated completion
and the matching fence bytes in render order — and carries **no authority**: a
fence with no authenticated event behind it does nothing at all (decision 1
carve-out). The kernel requires the fence to be present (non-zero); the shell's
script must write exactly the same bytes to the terminal.

**Why the id is optional, and it is not a convenience.** A shell that attaches to
an attempt the editor submitted never learns the app-minted id — `start` attaches
precisely by _not_ naming one, and there is no outbound envelope that would tell
it (§3 allows exactly two, `accept` and `refresh_request`). A required id here
made completion unreachable from the shell on the primary path. So the id is
omitted and the kernel resolves the domain's single open attempt by context,
which is unambiguous because the kernel permits at most one open attempt per
domain. When an id **is** present it must be that attempt; a foreign or unknown
id is still rejected, so optionality did not loosen the cross-attempt rule.
Found by implementing the shell side against this document, which is what that
implementation is for.

Validation: the attempt must exist, be open, and belong to the **active** domain
— a completion for a suspended, closed or lost domain, or for another domain, is
rejected ("an attempt cannot complete across domains"). The exit status is set
exactly once: a second `complete` for the same attempt is rejected.

**Abandonment.** Nothing may mark an attempt successful and nothing may assign it
an exit code it did not report (decision 5). An open attempt becomes `unknown`
only on: transport loss of its domain, closure of its domain, an authenticated
snapshot that cannot recover its completion, or an explicit abandon (native-mode
escape). Absence of a completion is not a timeout — commands legitimately run for
hours.

## 9. Domains: the stack

A lane holds a stack of domains, bottom (oldest) to top (newest); the top is the
**active** domain when its state is `Established`. States:

| State            | Meaning                                                                 |
| ---------------- | ----------------------------------------------------------------------- |
| `Pending`        | Minted; awaiting authenticated `hello`. No lifecycle events accepted.   |
| `Established`    | Past `accept`. The only state that can be active.                       |
| `Suspended`      | Yielded the lane to a child; not active. Events rejected.               |
| `Desynchronized` | Auth held, authority suspended pending an authenticated snapshot (§10). |
| `Closed`         | Ended cleanly (`domain_closed`) or revoked (budget exhaustion).         |
| `Lost`           | Its transport died, or its parent chain was lost.                       |

Transitions are authenticated events, never frontend stack guessing (decision 2):

- **`domain_suspended`** — the active domain yields. It stays in the stack; the
  lane has no active domain until a child establishes or the parent is
  re-activated. If an attempt is open in the suspending domain it stays open,
  suspended with it.
- **Establishment of a child** (parent's `hello` accepted) — requires the parent
  to be `Suspended` (a child established over an active parent is rejected: the
  adapter must suspend first) and requires the parent to be the top of the stack
  (the chain is linear — one child at a time). The child is pushed and becomes
  active.
- **`domain_activated`** — the top-of-stack suspended domain reclaims the lane. If
  it has an open attempt the lane goes `Running(attempt)`, else `PromptReady`.
  Activation is the _only_ way a suspended domain returns.
- **`domain_closed`** — the top-of-stack domain ends. It is removed; its open
  attempts become `unknown`; the lane has no active domain (a suspended parent
  below does **not** auto-activate — it needs its own authenticated activation).
- A **top-level** domain (no parent) requires the lane to have no live domains;
  one root per lane at a time.

**Nesting is a request/grant, and the stream is owned one domain at a
time.** The parent shell detects the nested command (sudo/su/ssh) in its
preexec hook, sends `domain_request` (`env` names the environment; `host`,
`user`, `port` carry the ssh destination), and blocks reading the channel.
The kernel validates the request (parent active and top-of-stack; known env
kind; ssh requires a host) and answers `domain_grant` — addressed to the
PARENT's connection — carrying the request echo plus, once the publisher's
grant seam has minted the child (`kernel.RequestDomain`, the kernel stays the
sole minter) and composed it, the child's domain id, epoch and an **opaque,
already-substituted bootstrap** the parent executes verbatim. The bootstrap
is opaque text: the parent never parses it, the per-epoch capability rides
inside it (never in the environment), and an **empty bootstrap is the
refusal** — the parent runs its command conventionally, never suspended
under a child that cannot exist (forwarding refused, sudo policy, an
unsupported shell).

The stream-ownership interval, both ends named (the descriptor-handoff
invariant the adapters must not break):

> The **parent** owns the channel stream from its `hello` until it has read
> the grant for its request. It then sends `domain_suspended` (a child's
> `hello` requires the parent Suspended — never exec the child before that
> frame is written) and execs the child with the bootstrap. The **child**
> owns the stream from its exec until the child process exits. The parent
> resumes at its next prompt boundary and sends `domain_activated`; only
> that authenticated activation restores it — a close alone must not.

And the failure interval, which is the one that bites:

> The child never establishes (bootstrap refused, sudo policy, no
> forwarding). The parent still resumes at its next prompt boundary and
> still activates — the kernel accepts an activation of a parent whose
> child never established (a Pending child is not on the stack), and a late
> frame from that stillborn child is rejected against the restored parent.

Delivery of the bootstrap is per environment: for **sudo/su** (same
machine) the parent stages it and the child reads it through a **preserved
descriptor** (`sudo --preserve-fds=N … --rcfile /dev/fd/N`); the per-epoch
capability never enters a filesystem object (ADR-0024's own preference,
recorded in its open-questions section). For **ssh** the bootstrap is a
**rewritten command line** the parent executes — ADR-0022: the ssh command
line is the carrier — carrying the child's forwarded lifecycle port as a
reverse forward on that same ssh connection plus the in-band install
payload. Where the local platform cannot preserve the descriptor (sudo
policy, su that closes fds), the honest fallback is the conventional
terminal, visible in the product. `docker exec` is conventional-only by
owner decision: neither an inherited descriptor nor a loopback port crosses
the container boundary — that is a container transport of its own.
Events from a suspended, closed or lost domain are rejected against the active
lane — stale events can never touch the live domain.

## 10. Desynchronization and the snapshot

**Entry.** The adapter reports a framing gap (`notify_gap` with the scanned
garbage bytes/frames). The domain becomes `Desynchronized`; if it was active the
lane lifecycle becomes `Desynchronized(domain)` — editor ownership is revoked,
input routes raw, the terminal stays visible, and ordinary lifecycle events are
quarantined (rejected, nothing mutated). The kernel emits `refresh_request` with a
fresh request id, and the adapter restores a visible prompt immediately (decision
9: a `Desynchronized` domain is not live; waiting for "the next prompt" at a
suppressed prompt would produce an invisible prompt taking raw input).

**Exit — only a snapshot answering nocx's own refresh request restores authority**
(decision 7). `snapshot` carries:

- `request` — must equal the outstanding refresh request id; otherwise rejected.
- `shell_state` — `at_prompt` | `running`.
- `active_attempt` — the attempt the shell believes is running, if any.
- `last_completed` — the last completed attempt and its exit code, if any.
- `next_seq` — the shell's next sequence number; must be strictly greater than the
  kernel's last accepted sequence for the domain.

Reconciliation (never inventing success):

- an open attempt of the domain that the snapshot does not name as active and does
  not report completed → `unknown`;
- an open attempt named as `last_completed` → completed with the reported code;
- an `active_attempt` not currently open → a shell-originated attempt is created
  open with that id (the `start` was lost in the gap);
- the domain's expected sequence becomes `next_seq`; the domain returns to
  `Established`; the lane returns to `Running(active_attempt)` or `PromptReady`.

The budget (§6) bounds how long authority can stay revoked; exhaustion revokes the
domain. Repeated desync episodes are themselves budgeted (3 per domain lifetime).

## 11. Sequence and replay

- The counter is **per domain within its epoch**; the shell increments it on every
  envelope it sends.
- After authentication (version, domain, epoch, capability all valid), the kernel
  rejects duplicate or decreasing sequences (`seq <= last_accepted`). Gaps are
  permitted (strictly increasing, not consecutive).
- Sequence state mutates **only after** authentication: a wrong-capability frame
  with a high sequence advances nothing.
- A reconnect never resets the counter within an epoch; a new epoch means a new
  capability and a reset counter (fresh domain, fresh counter from 0).

## 12. Loss

Two losses, two code paths (decision 8):

- **Transport loss** (`transport_lost`) ends every domain bound to that transport:
  domain → `Lost`, capability and listener end with it, open attempts → `unknown`,
  and any lane whose active domain died falls to `Lost`. The cascade: a domain
  whose parent chain contains a lost domain is lost too (a child cannot outlive
  the parent it nests in). A new session gets a fresh epoch — never a resumed one.
- **Frontend/backend reconnect** is the publication layer's concern: it must
  either resume the existing domain or report ambiguity and revoke it. The kernel
  model does not reset on it.

In one local transition nocx revokes ownership, exposes the terminal, marks open
attempts `unknown`, and stops accepting events for the dead domain.

### 12.1 Restoration: the composite acknowledgement

This section is the home of the mechanism ADR-0024 decision 8 deliberately left
open (`nocx-u7uh.20`): the ADR fixes the property — restoration is acknowledged
before a session is treated as a usable conventional terminal — and the composite
acknowledgement below is how it is met. The ADR carries a pointer and the two
rejected alternatives; the bytes stay here.

Decision 8 distinguishes two losses, and the **session coordinator** (the
transport, not the kernel) tells them apart by two independent signals:

- the lifecycle adapter dies while the session channel's `Done()` is still
  open → **restoration is pending**; the sequence below runs;
- the pty/SSH channel `Done()` closes → the session is dead: emit `exit`,
  cancel any pending restoration, reject late acknowledgements, report a
  disconnected terminal, and make **no restoration claim**. If the two race,
  session death wins.

The kernel never distinguishes the two and never tries: on either signal the
domain is `Lost` and the lane falls to `Lost` — the atomic local transition
of decision 8 — and a new establishment is a fresh epoch.

**Restoring the user's visible prompt is a protocol action, and it can only
be promised while the shell is reachable.** Over a dead connection the
promise is not made. Over a live one the sequence is:

1. The channel dies, the pty lives. The lane is `Lost` (authority revoked at
   that instant) and the session enters **RecoveryPending**.
2. The renderer applies the conventional presentation: native input, live
   region released, block model off, editor withdrawn — and offers no
   editor anywhere inside the span.
3. At the next prompt boundary the shell notices its send failed, clears its
   active latch, restores the native `PS1`, and writes a **one-shot recovery
   fence** to the pty immediately after the prompt bytes.
4. The renderer matches that explicit fence. It does not inspect the grid,
   pattern-match a prompt, or infer from silence.
5. Only after **both** the fence matched and the presentation is applied
   does the renderer acknowledge — the narrow `lifecycle.recoverAck`
   carrying only session identity and the recovery generation — and the
   lane may fall `Lost → Native`.
6. The domain stays permanently `Lost`; any future integration is a fresh
   epoch, never a resumption.

**The fence.** Each domain mints, alongside its capability, a distinct
**recovery nonce** (32 random bytes): pre-provisioned, one-shot, handed to
the shell in the authenticated bootstrap while the channel was alive — never
the capability, never reused. The shell writes it to the pty only when the
channel died mid-session. This is the decision-1 carve-out, the same
rendezvous the completion fence rides: a stream sequence may _locate_ an
already-authenticated lifecycle event in render order, and may never create,
authenticate, complete or assign status. The recovery fence locates the
restoration — it does not create one, and it is not a new stream-derived
authority edge: a hostile program cannot forge what it never saw, and the
worst a forged fence could do is force a safe transition to native mode,
which decision 10's availability bound already accepts ("a descendant that
can write to the lifecycle transport may force a safe transition to native
mode. It can never produce a validated event without the epoch's
authenticator").

**The acknowledgement** (`lifecycle.recoverAck`) is deliberately narrow:
session identity and the recovery generation, nothing else. The backend
accepts it only while that exact session is RecoveryPending and alive; it
permits only `Lost → Native` (it can never revive a `DomainLost`, never
grant ownership, never open or complete an attempt); it is idempotent; and
it is invalidated by session exit — a late acknowledgement after the session
died is rejected.

## 13. Security boundary

This protocol defends against hostile bytes on the terminal from any source
(ADR-0024 decision 10). It does **not** defend against a compromised shell or
shell plugin, a same-user process that can inspect the shell's memory or the
bootstrap, or a compromised backend. We authenticate who spoke; we can never prove
a compromised speaker told the truth.

## 14. The kernel (`internal/lifecycle`)

The kernel is the pure model: no transport, no shell, no `internal/pty` or
`internal/ssh` imports; testable with a fake transport. It exposes:

- `BindTransport(id, port)` — registers a transport; `port.Send` carries outbound
  envelopes (`accept`, `refresh_request`).
- `RequestDomain(lane, parent, transport)` → `{domain, epoch, capability}` — mints
  a `Pending` domain bound to the transport.
- `Ingest(transport, envelope)` — validates (version → domain → epoch →
  capability → sequence → legal transition) and applies; invalid events mutate
  nothing.
- `NotifyGap(transport, domain, bytes, frames)` — desync entry + budget
  accounting.
- `TransportLost(transport)` — §12.
- `SubmitAttempt(domain, command, cwd, host)` — app-originated attempt.
- `AbandonAttempt(id)` — explicit `unknown` (native-mode escape).
- `State(lane)`, `Attempt(id)`, registry lookups — read model for the publication
  layer; no current-domain singleton accessor exists anywhere.

The frontend receives published facts (a separate bead, `nocx-u7uh.13`/`.5`), not
raw frames and never a capability.

## 15. Agent enrolment: the interval a backend grid lives in

- **Implements:** the AD-6 amendment in [`architecture.md`](architecture.md) ("A live
  grid for an enrolled pane"), and D5/D13 of the orchestration mechanism design.
- **Bead:** `nocx-szb40.5`.

The backend may keep a live VT grid for a pane, so that what a pane is doing can be read
while no client is attached. The amendment permits it only inside an **interval**, and
says the interval opens by an **act** — a control-plane call naming a pane — and never by
an inference that a title or a command word looked like an agent, because an inferred set
has no upper bound and no audit. This is that act.

### 15.1 Why it is on this channel and not a socket of its own

The orchestration design's §7.1 says "requests enrolment over the private local socket",
which reads as a new transport. It is not one, and building one would be a mistake in two
separate ways. It would be a **second authenticator** for the same trust decision, next to
the one decision 2 already built and §13 already bounds. And it would not work: the
per-epoch capability is "substituted into the integration script text, never passed as an
environment variable" (§4), so a separate binary launched by the shell can inherit the
descriptor and cannot authenticate on it — possession of the transport is not possession
of the domain.

That is why the caller is a shell **function inside the integration bundle** rather than
the `nocx agent run` binary §7.1 describes. The binary's reason to exist is the rest of
§7.1 — staging an agent's config, and holding a pid a dispatcher enrolment can be pinned
to — and both belong to `nocx-dkawo`, which is about authority. A grid grants no
authority: it asks nocx to watch a pane the caller is already sitting in.

### 15.2 The pair, and both ends of the interval

`agent_enrol` names what is about to run and the geometry to start at. It does **not**
name the pane: the pane is the envelope's lane, which the kernel authenticated, so a
caller cannot enrol a pane that is not its own.

The geometry comes from the shell rather than from the pty because at this point in the
backend nothing owns "how big is that pane" — no read model answers it — and inventing a
second owner of a pane's geometry to answer it once would be worse than taking the number
from the process that is about to _be_ that pane. It is a **starting value, not the
authority**: every subsequent window change reaches the grid from the pty's own path,
which is authoritative, so a wrong start is corrected by the first resize rather than
believed forever. Both dimensions are bounded (1..1000), because a grid is a real emulator
with a real allocation and the number arrives from a shell.

`agent_withdraw` closes the interval. It is the end the caller knows about — the agent it
bracketed has returned, however it returned. The backend closes the same interval again
when the session's output ends, which is the end that covers a caller that was killed
rather than one that returned. **Two ends, and neither is optimistic.**

The wrapper **brackets** the agent rather than `exec`ing it, which is where this differs
from §7.1's step 5. `exec` preserves a pid so a pin can survive it; that pin is what a
dispatcher needs and not what a grid needs, and bracketing buys the second end above.

### 15.3 The verdict is fail-closed on the wire, not merely in the code

`enrolled` is on the wire **only when it is true**. A refusal carries no such field at all.
That is deliberate and it is what makes the fail-closed rule structural rather than
careful: a truncated frame, a frame from an older backend, a frame half-composed by
something hostile — anything a reader does not positively recognise as consent is a
refusal, with no parsing that has to go right for the refusal to be reached.

The trap, which caught the first version of the test that asserts this: the **event kind
is itself `agent_enrolled`**, so a reader matching the bare word `enrolled` finds consent
in every refusal ever sent. Match the field — `"enrolled":true` — never the word.

An unwired backend refuses everything. This is the opposite of §9's grant, where an
unwired builder answers with an empty bootstrap and the parent runs its command
conventionally — right for an optional enhancement, wrong for something an invariant rests
on (D4: no enrolment, no orchestration).

### 15.4 What a refusal does, and what it does not do

The refusal is **printed in the pane**, with the backend's own sentence, because "the pane
says so" is not satisfied by a log line the person never reads — that is the silent-degrade
shape `AGENTS.md` names, where a feature that does not exist survives a release behind a
warning nobody sees. Every refusal the backend can produce is therefore a sentence, not a
code.

And the agent **still runs**, unorchestrated. "Failure is closed" means no enrolment
implies no orchestration; it does not mean a terminal declines to start the program its
user asked for because a feature of its own is unavailable. A bare agent started outside a
nocx panel session is the same case and reads the same way.

### 15.5 What enrolment may never decide

Enrolment is not lifecycle state. It may not open, complete or alter an execution attempt
(ADR-0024 decision 1), and the kernel keeps nothing about it — it authenticates the frame,
which is the one thing only it can do, and the fact then belongs to whoever owns grids. A
grid answers what is on the screen and where the cursor is, and nothing else; the two
decisions the amendment permits from one — may nocx type here, what does the indicator show
— are made by callers reading a frame, never by the grid and never here.
