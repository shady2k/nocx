# Level 1 — the helper owns the host

## 0. Status, and what this supersedes

Written after the architecture turn of 2026-08-31, in which the owner settled three
questions that the earlier drafts of the same day had answered differently or not at all:

1. **The script substrate stays.** The helper is an ADDITIONAL level, not a replacement.
2. **A tab on the host outlives the process inside it** — but that promise belongs to
   level 2, not here.
3. **Any nocx under the same Unix account may reach the helper.** Same-UID trust, frozen.

Three drafts from earlier that day survive as HISTORY and are not rewritten:
`2026-08-31-the-helper-as-execution-host-design.md`,
`2026-08-31-the-generation-daemon-lifecycle-design.md`,
`2026-08-31-content-store-restart-reconciliation-design.md`. Where their decisions
survived the turn intact this document cites them rather than restating them; where the
turn changed one, this document says so and wins.

Backlog: `bd list --label remote-host`. This document is level 1 — `nocx-k6p18` and
`nocx-wrugm`, with `nocx-457v` and `nocx-m7k90` riding on the surface it establishes.
Level 2 is its own document.

## 1. What a user can do that they could not before

**On a host they have opted into, blocks, git, the file tree and completion work the way
they do locally — with no rc file edited and no script uploaded — and the program they
started is still running when they come back, including across a nocx update.**

The end-to-end check that watches them do it is `nocx-k6p18`'s acceptance criterion, and
the update half is `nocx-wrugm`'s.

## 2. Three levels, and why the lowest one stays

| level | what it is                                                         | install    | consent                  | what it gives                                                                                       |
| ----- | ------------------------------------------------------------------ | ---------- | ------------------------ | --------------------------------------------------------------------------------------------------- |
| **0** | the script substrate — OSC 7/133 through shell hooks (AD-5 Tier A) | none       | none                     | blocks and cwd on any host you can reach                                                            |
| **1** | **the helper** — spawns the shell, owns its PTY                    | by consent | machine-keyed (ADR-0034) | native git, filesystem, environment, completion; the session outlives the connection and the update |
| **2** | the server on the host                                             | on request | its own, separate        | durable tabs with their blocks, visible from any of your machines                                   |

**Level 0 is not deprecated and must not be.** There are hosts where nothing may be
installed — a policy that forbids it, a read-only filesystem, a container you are inside
for thirty seconds, a box you are looking at once. Blocks must work there. This is also
what preserves AD-5's stated purpose ("Prevents: coupling MVP features to a remote-install
requirement") through the amendment level 1 makes to it.

**So the product always knows which level a tab is on, and says so.** A tab that cannot
survive a disconnect must not look like one that can. This is the same rule as the
soft-degrade rule in AGENTS.md, applied before the degrade rather than after it.

**And the helper is the INTEGRATION first, a session keeper second.** This ordering is the
owner's and it is the right one: level 1 would be worth shipping if reattachment did not
exist at all. Framing it as "tmux for nocx" understates it and mis-sequences the work.

## 3. What this crosses

**AD-5** — amended, and NARROWLY. Tier B "augments (never replaces) the remote shell" is
true of every host without a helper, and false of one with it, because the helper spawns
the shell. The amendment lands in `docs/architecture.md` with `nocx-k6p18` and is not
routed around. AD-5's purpose survives whole (§2).

**AD-7** — session identity is server-authoritative. Amended: the execution host mints it,
and the durable handle names the generation that minted it (D2). "The server" becomes
"that host's helper", which is the same shape AD-7 protects — one authority, not the
frontend.

**AD-1** — untouched, and this is load-bearing. The binary data plane is not re-wrapped:
the ordinary WebSocket rides an ssh channel unchanged (§5). No PTY byte is ever wrapped in
JSON-RPC.

**AD-6** — untouched. The backend still never sniffs the stream. The helper reads bytes to
MOVE them, never to interpret them; every marker-derived fact still crosses as a typed
record from the renderer, per AD-1's 2026-08-02 amendment.

**AD-9/AD-10** — the window's bound and the straggler rule, decided in the execution-host
draft's D8 and unchanged by the turn.

**ADR-0034** — consent stays keyed to the machine, and level 1's grant is the HELPER's
footprint only. Level 2's is separate and is not implied by it.

**ADR-0043** — one connection to the encrypted store, and the turn made it easier rather
than harder: only one coordinator ever holds `content.db`, so the multi-process question
this ADR left open does not arise here.

## 4. Decisions

### D1 — The binding promise

**A coordinator being replaced — by an update, a crash or a quit — does not end a session
and does not end the ability to reattach to it.** The helper holds the PTY; the coordinator
is replaced underneath it.

The promise is bounded in one place and stated rather than discovered: **output produced
while no coordinator was attached is kept only up to the helper's window.** Beyond that it
is gone, and the gap is reported (D6). This is the lossy-continue decision of `nocx-8mllr`
D6-b, and it is what makes the promise implementable without a disk on the host.

### D2 — Two identities, and the ledger only ever names one

The turn's sharpest correction. Before it, "the session" meant the coordinator-owned PTY
channel, so its death was the session's death. That is now false, and conflating the two is
what makes a replacing coordinator delete live work.

```
hostSessionId   the PTY and its process group, minted by the helper,
                qualified by the generation that minted it.
                SURVIVES coordinator replacement.

attachmentId    one coordinator↔helper connection and its lease.
                Disposable. Appears nowhere in the ledger.

streamOffset    the monotonic output coordinate of a hostSessionId.
                Survives attachments; a new reader does not restart it.
```

`entries.session_id` references `hostSessionId`. The recording's offsets are that session's,
not that connection's. **A new attachment is a new reader of the same stream, never a new
stream.**

**And the helper's identity is not the pane's.** The ledger holds a BINDING from pane to
`hostSessionId`; they are not the same key. One host session may be projected into several
panes — which is what level 2 needs and what level 1 must not foreclose.

### D3 — What the helper owns, and what it must never own

Owns: the PTY and process group, the OS-specific operations (git, filesystem, environment,
completion inputs, and later Seatbelt and Landlock), the bounded output window, the session
inventory, exit status, signals, and the enforcement of one writer.

**Never owns: blocks, the ledger, `content.db`, UI state, product policy, or a human-authored
name.** The execution-host draft's D2 states this as "fat infrastructure, thin product" and
the turn did not change it. The measured reason is in that draft's §6: ~2.8 MiB against
~40 MiB, and a survival component that must stay compatible across generations has no
business carrying SQLCipher and a vault.

**On names specifically** (settled against an earlier proposal of mine, and against codex's
first recommendation): the helper may report DERIVED diagnostics — cwd, argv, foreground
process, start time — because those are facts about a process and the OS is their source.
It may not persist a name a person typed. In level 1 a friendly alias is a local projection
owned by the local server; in level 2 the host's ledger becomes its owner. One owner ever.

### D4 — Generations coexist; the coordinator retires them

Unchanged from the generation-lifecycle draft (its D1–D8) and the execution-host draft's
D4–D6, which the turn left intact. In one paragraph: installs are content-addressed and
immutable, so two generations are resident at once; a generation lingers exactly as long as
it holds a session; new sessions go to the newest; retirement is rename-to-tombstone so a
generation being removed can never be selected; and liveness is a kernel fact (a lock), never
an inference from an error.

What the turn added: **this, and not a coordinator handover, is how an update stops costing
you your sessions.** `nocx-op33k` proposed handing sessions between two coordinators and was
closed as superseded — there is nothing to hand over, and there are never two coordinators
against one store.

### D5 — The startup sweep may no longer judge, and there are three answers

`internal/content/sqlite.go`'s `Open` runs two sweeps, both founded on a premise stated in
the code: "a session is server-authoritative (AD-7), lives inside one backend process and
**cannot outlive it**". D1 repeals that premise. Left alone, a replacing coordinator would
delete the `sessions` row of a running session, delete its recording, null the `session_id`
of every entry naming it, and close its open entry as `unknown` — declaring finished a
command that is still running.

**I argued during the turn that this needed only one `UPDATE` — stop forcing `phase='closed'`
and keep both deletes, because "the pipe died". That argument is wrong and is recorded here
so it is not made again.** The pipe is the ATTACHMENT (D2). The session and its output stream
survived. Deleting the recording because a reader went away throws out precisely what the
promise exists to keep: a coordinator that recorded thirty minutes of a build, updated, and
returned after the helper's window had rolled would have destroyed those thirty minutes, and
"start a fresh recording" avoids the discontinuity error only by discarding them.

So the sweep is replaced by reconciliation with **three verdicts**:

| verdict     | meaning                                                             | action                                                                     |
| ----------- | ------------------------------------------------------------------- | -------------------------------------------------------------------------- |
| **live**    | the exact generation reports this host session                      | keep everything: row, recording, provenance, and the open entry stays open |
| **absent**  | the exact reachable generation was asked and says it does not exist | exactly what `Open` used to do, for that session alone, in ONE transaction |
| **unknown** | nobody could be asked                                               | change nothing; reconcile on a later attempt                               |

**`unknown` may never collapse into `absent`.** A refused connection, a timeout, a sealed
vault and an unreachable host are each `unknown`. `absent` requires an answer, never an
error. This is the reconciliation draft's D1/D3 and it survives the turn intact.

**Ordering** (codex retracted its own first answer here, and the corrected order is the one
the reconciliation draft already had): `Open` cannot ask, because asking needs a carrier,
the carrier may need the vault, and the vault needs the store. So `Open` opens and judges
nothing; carriers come up; inventory runs; each session is reconciled. A pending set in
memory is sufficient — a durable `unreconciled` stamp is optional and buys only the ability
to display cause and age, and to bound recordings of hosts that never return.

**`pane_id` is untouched in every branch.** It is the anchor; a block whose session is gone
is still a command that ran, in the pane it ran in.

**One bound is owed here**, and it is the reconciliation draft's D6: `dropDeadSessions` was
the only bound on session recordings — they are deliberately outside the budget sweep because
their unit is not `artifacts.byte_len`. The `absent` path restores it for the ordinary case;
the `unknown` case needs a retention age, or a host that never comes back accumulates
recordings forever.

### D6 — The recording resumes with an honest hole

`Append` refuses any discontinuity today, and `session_output.go` calls a gap "a caller defect
and not a fact about the stream". That is right while a recorder cannot be absent. It stops
being right here.

`SessionOutputRepository` gains one operation: **`Skip(sessionID, resumeAt, reason)`** — it
advances the recording's produced cursor across a range that was never offered, records the
gap in the same `Gap` shape the cap already uses, and **leaves the recording appendable
afterwards**. Without that last clause one missed second costs the whole session, because
`ws_session_record.go` stops recording permanently once it loses its place.

**The reason is a second value, not `cap`.** Telling a user the cap dropped bytes nobody ever
had is a false statement in the product. `session_output.go`'s sentence is amended rather than
contradicted: a gap is a caller defect **when the caller had the bytes**, and a fact about the
stream when nobody did.

`Produced` already measures the hole — "including what was dropped … what makes a hole
measurable rather than invisible" — so nothing parallel is invented.

### D7 — The exit code must still be able to land, and removing the forced close is not enough

Found by codex and not by me: dropping `phase='closed'` is necessary and insufficient. Today a
lifecycle transport loss makes the domain `Lost` and its open attempts `unknown`
(`docs/lifecycle-protocol.md`), so nothing would ever deliver the real exit status to the row
we kept open. One `UPDATE` cannot supply an authority that does not exist.

**Chosen: authenticated snapshot recovery, with the helper owning continuity and not the
kernel.** The helper retains, in memory, for the PTY's lifetime: the lifecycle rendezvous, the
epoch/capability material needed to authenticate a recovery, the session association, and
enough sequence state to reject a stale one. A replacing coordinator sends a `refresh_request`
through that rendezvous and rebuilds its kernel from the shell's authenticated
`active_attempt`, `last_completed` and `next_seq` — a shape `docs/lifecycle-protocol.md`
already defines.

Rejected, with reasons:

- **Move the lifecycle kernel into the helper.** Imports block and attempt POLICY into the
  execution host, against D3 and against the size argument behind it.
- **A durable event journal on the host.** A second ledger, with its own retention, replay,
  migration and generation-handoff problems — for a question a snapshot answers.

**The invariant this freezes, and it must be stated because the snapshot is only sufficient
under it: no new top-level command may begin while no writer is attached.** One command may
finish during the absence; the shell then waits at its prompt, which `active_attempt` plus
`last_completed` fully describes. If autonomous submission with no coordinator present is ever
wanted, a single snapshot stops being enough and the journal becomes unavoidable. That is a
product decision, taken here, deliberately.

### D8 — Many readers, one writer, and the identities are frozen now

Level 2's product rules about who may type are not decided here. What IS decided here, because
an old generation's ABI cannot be changed later:

- subscriber identity on attach, data and write;
- independent 64-bit read cursors per subscriber;
- `fresh` as an explicit flag on attach, never inferred;
- gap and reset semantics;
- exactly one write capability, carrying a lease epoch.

A generation whose ABI assumes one unnamed client can never serve two observers correctly, and
generations live for months. The mechanics are the generation-lifecycle draft's D5 and the
execution-host draft's D7; the turn only moved the deadline for freezing them into level 1.

### D9 — Three verbs, and level 2 may not reinterpret one of them

| verb            | what it does                                                                            |
| --------------- | --------------------------------------------------------------------------------------- |
| `detach`        | drop this attachment. The process survives; it is in the inventory and reattachable.    |
| `close-session` | deliberately end the helper-hosted session.                                             |
| `uninstall`     | revoke machine consent and, after naming every live process and asking, end everything. |

Level 2's `close-tab` is NOT a fourth helper verb: it mutates a durable container on the host,
and whether it also requests `close-session` is a separate explicit choice made there.

**In level 1, closing a tab is `detach` plus losing the local tab.** The process is not lost —
it is in the inventory. And "losing the tab" is weaker than it sounds: `DeleteTab` already
marks the tab and its panes closed and **keeps every row**, precisely so an ordinary Cmd-W does
not destroy block history, so the blocks stay in recall.

Uninstall is whole or not at all (the execution-host draft's D14, unchanged): no partial
removal, the running processes are named before anything happens, and consent is revoked.
**Helper uninstall must never silently define deletion of a level-2 `content.db`.**

### D10 — Discovery is an inventory the helper serves

There is no router and no registry beside the helper: it holds the PTYs, so it is the only
thing that can answer. The reserved-and-unimplemented `session` service in
`internal/helper/host/host.go` — `Register` panics on the name — is where this lands.

An entry carries `hostSessionId`, its generation, start time, and the derived diagnostics of
D3. **`/proc` and `proc_pidinfo` are EVIDENCE, not authority**: argv is mutable, a process can
be replaced, and the semantics differ per OS. Launch metadata is recorded explicitly by the
helper when it spawns; OS inspection is a fallback and a cross-check, never the canonical
identity.

### D11 — One shape locally and remotely

The helper runs on every machine, including yours. Locally the coordinator connects to its
endpoint directly; remotely through the bridge (§5). There is no second mechanism, no
"local special case", and no code path that exists only for one of them — which is the
requirement the owner set at the start of the turn.

### D12 — The trust boundary, frozen deliberately

**Any nocx running under that Unix account may connect to the helper.** Same-UID trust; no
session capability is reserved.

Stated rather than left to be discovered: **any process running as you on that machine can
attach to your sessions and write to them.** On a machine you own this is the same bar as
everything else in that account — your ssh keys, your shell history, your files.

This cannot be retrofitted. A generation deployed without a capability will run for months,
and an opaque identifier can never later become authorization. If independent same-UID nocx
servers must ever be isolated from one another, the capability is owed **now**. The owner
decided on 2026-08-31 that they need not be.

## 5. The carrier

**SSH is a transport for reaching the helper, not the terminal protocol.** The ordinary nocx
WebSocket rides inside unchanged — the same relationship `internal/apisend/ssh_dialer.go`
already has with HTTP. AD-1 is therefore untouched: the binary plane is not re-wrapped in
JSON-RPC, it is the same plane on a different socket.

**The authoritative endpoint is a private Unix socket** in a `0700` directory, mode `0600`.
Not a loopback TCP port: a port on `127.0.0.1` is reachable by ANY user of that machine, and
the whole authorization model is the Unix account (D12), so a loopback listener would annul it.

**Remotely, nothing is forwarded.** `nocx-helper bridge <generation>` runs over the pty-less
exec lane, connects to the endpoint on the far side and copies bytes; it is stateless and
disposable, holding no session, no window and no lock (the generation-lifecycle draft's D6).
`direct-streamlocal@openssh.com` is an optional carrier improvement and never the boundary —
this corrects my own claim during the turn that it was a security prerequisite.

**And the ssh carrier needs no authentication of its own**, because reaching that socket
already required becoming the account. A non-ssh carrier must supply one; that is the carrier's
problem, not the protocol's.

## 6. Assertions

1. **A live session survives `Open`.** With a session running on a reachable generation, a
   fresh coordinator deletes no `sessions` row, deletes no recording, nulls no `session_id`
   and closes no entry.
2. **Its open entry stays open**, and is not stamped `unknown` while its process runs.
3. **An absent session is reconciled exactly as `Open` used to sweep it**, in ONE transaction —
   a reader never sees the entry closed while the execution is not.
4. **A failure is never a verdict.** Refused connection, timeout, sealed vault and unreachable
   host each leave the session unreconciled. Asserted per failure mode, not once.
5. **`pane_id` survives every branch.**
6. **Reconciliation is idempotent and resumable**: killed at each step in turn, the next pass
   completes it and no row is half-judged.
7. **`Skip` records a gap and the recording survives it**: `Produced` has advanced, the gap is
   in `Gaps`, and subsequent appends succeed.
8. **The gap's reason distinguishes a cap eviction from a never-ingested range**, and the
   product's wording differs.
9. **The thirty-minute case**: record ≥30 min of a running command, replace the coordinator,
   let the helper's window roll past the recorder's cursor, reconnect — the first thirty
   minutes are still readable and the hole between them and the resumed tail is explicit.
10. **The exit code lands after a replacement**: start a command, replace the coordinator, let
    the command finish, and the block that was open receives its real status — not `unknown`.
11. **The frozen invariant is enforced, not merely written**: a submission attempted with no
    writer attached is refused.
12. **One writer**: a second write-capable attachment is REFUSED, never silently promoted; a
    frame carrying a stale lease epoch is rejected.
13. **Two observers read independently**: one stalling does not throttle the PTY or the other,
    and is reset to the window base rather than slowing the producer.
14. **Level 0 still works**: on a host with no helper, blocks still arrive through the script
    substrate, and the product says which level the tab is on.
15. **`detach` is not `close`**: closing a tab leaves the process running and findable in the
    inventory; `close-session` ends it; `uninstall` names every live process before doing
    anything.
16. **Uninstall leaves nothing**, and consent is revoked with it; a level-2 `content.db` on
    that host is untouched.
17. **And the paired positive** (AGENTS.md's "for every returns-an-error there is an
    and-on-a-normal-machine-it-succeeds"): on an ordinary reachable host, a nocx update leaves
    the command running, the block open, the recording continuous where the coordinator was
    present and explicitly holed where it was not, and the pane restorable.

## 7. Deliberately out of scope

- **Durable tabs on the host, and anything a second machine sees** — level 2's document.
- **Who may type when two clients watch** — the mechanics are frozen here (D8), the product
  rule is level 2's, and `nocx-eidfb` currently answers it differently ("last to attach wins")
  than the controller-lease model level 2 assumes. That conflict is open and is the owner's.
- **Migrations** — not needed while `content.db` is local and disposable. They become a
  prerequisite of level 2 (`nocx-lmb6v`), not of this.
- **The word `relay`** — removed from the vocabulary; 411 occurrences remain in code and ADRs,
  including type names such as `RelayConsent`. Its own chore, not smuggled into this work.

## 8. Open questions

1. **The retention age for unreconciled recordings** (D5's owed bound) — a setting with a
   default; choosing the default is ordinary product work.
2. **Whether the helper's inventory is asked once at start or continuously**, and what the
   product shows in the window between "carrier up" and "inventory answered".
3. **`nocx-eidfb`'s resize rule versus the controller lease** — see §7.
