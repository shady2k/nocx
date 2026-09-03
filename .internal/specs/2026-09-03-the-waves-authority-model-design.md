# The wave's authority model: membership is not delegation

- **Date:** 2026-09-03
- **Bead:** `nocx-z3nkr`, a child of `nocx-dkawo`
- **Status:** draft, awaiting owner review
- **Closes:** open questions 1, 2, 3 and 4 of
  `.internal/specs/2026-08-24-orchestration-mechanism-design.md` §10
- **Reads with:** `.internal/specs/2026-09-03-mesh-and-what-progress-may-decide-design.md`,
  which decided the topology this document authorises

## 1. What this crosses, and what those documents already decided

**Cite decisions by their date.** The 2026-08-15 design and the 2026-08-24 design each have a
`D5`, `D8`, `D9`, `D13` and `D14`, and they are unrelated: 2026-08-15 `D8` is the delegation,
2026-08-24 `D8` is the four acknowledgements of mail. Every reference below carries its date.

**2026-08-15 `D8`** is the shape this document adopts rather than re-derives:

> **Lineage records provenance; a separate revocable Delegation carries continuing authority.**
> `createdBy(parent, child)` is immutable and proves only "A created B".
> `Delegation{controllerSession, childSession, epoch, effects, state}` proves "A may currently
> observe or control B", with effects `observe | receive-events | send-input | delegate-further |
close`.

Its §4.1 adds two fields the inline form omits — `createdByRunId`, labelled _(provenance)_, and
`state: active | input-suspended | scope-suspended | revoked | expired`. The holder is a
**controller SESSION**, decided against owning it from the parent run because "a coordinator run
ends while its workers live".

**2026-08-15 `D9`**: "Spawning a child is the `delegate` effect over the resource `environment`,
permitted only into an environment the workspace lists (or, per D3, the parent's own). Reaching
further is scope expansion and escalates."

**ADR-0020** records an immutable grant per run and, in its 2026-08-16 amendment, "the matrix
decides; the narrowed capability enforces". **ADR-0028 decision 4**: "The dispatcher narrows; it
does not check. A check before the call is advisory, because the tool still holds a full session
manager, the vault and the filesystem." **ADR-0053**: a tool declares the SET of effect classes
it can resolve to. **ADR-0045**: declared calls are the only carrier, and "a carrier is a way of
reaching that kernel, never a way around it".

**AD-8** puts every module behind one interface at one composition root. **AD-7** makes the
session the backend-owned identity, which is what 2026-08-24 `D3` hangs recovery on.

**One correction this document owes.** The 2026-08-15 design §8 deferred the grant-source seam
because "`StartExecution` — the whole ADR-0020 grant path in `internal/content/` — has no caller
outside its own package". That is **stale**: there are six live call sites
(`internal/transport/ws_ledger.go:577,599`, `internal/transport/ws_lifecycle.go:339,358`,
`internal/assistant/kernel.go:1132,1253`) reaching it through `internal/capability/ledger.go:125`,
and a live minter in `WSServer.runGrantFor` (`internal/transport/ws_readscreen.go:277-305`). The
seam that was deferred exists. Anything costed against that sentence should be re-costed.

**A second correction, smaller.** §10 of the 2026-08-24 design tags questions 1, 2 and 4 with
`nocx-z3nkr` and leaves question 3 untagged, while the bead's own description claims 1–4. The
bead is right — question 3 is the same topic — and §10 should carry the tag.

## 2. The problem, measured

`Delegation` does not exist in Go. `grep -rn "type Delegation\|AuthorityBinding"` over the tree
returns nothing; `input-suspended`, `scope-suspended`, `receive-events` and `delegate-further`
appear only in design documents. There is no contract schema for any of it.

What **does** exist is the run grant, and it is live: `content.Grant{Version, ExpiresAt, Policy,
Effects, Scopes}` (`internal/content/ledger.go:599-605`), persisted to `authority_grants` keyed
on `execution_id UNIQUE` (`internal/content/sqlite.go:809-838`) — **with no session column and no
revocation column**. That is the wall a continuing, revocable, session-held delegation has to get
past, and naming it is half of this document's work.

Three things are already built that this design must consume rather than reinvent:

1. **Lineage is built as a prohibition.** `internal/lineage/lineage.go:22-25` and
   `internal/session/lineage.go:7-12` both state that the edge "answers PROVENANCE ONLY … and
   nothing here reads an ancestry to decide whether an operation is allowed". Immutability is
   admission-time-only (`internal/session/lineage.go:87-92`) — "the check is at ADMISSION and is
   never repeated. That is deliberate and it is what makes the edge immutable rather than merely
   unwritten-again."
2. **Demotion is shipped, one scope down.** `internal/transport/ws_run.go:247-255` refuses a new
   run while `awaitingTakeover`, with the comment "the agent is demoted, not evicted — it loses
   write authority, so a new run is refused; reading (RequestScreen) is untouched", and
   `internal/transport/run_lease.go:656-673` suspends rather than kills. That is 2026-08-15
   `D8`'s "human takeover suspends `send-input` and preserves `observe`", already working at the
   lane. It is raised to the session here, not written afresh.
3. **The capability shape is shipped.** `agenttools.Runner`
   (`internal/agenttools/runner.go:7-63`) "holds EXACTLY the grant's `ResourceSession` scopes and
   nothing else … **because it never holds the identity of any other session**", and the proof is
   on the wire: `contracts/tools/session.run.schema.json` has no session parameter at all. The
   model cannot express "run in another pane".

## 3. Decisions

| #   | Decision                                                                                                                                                                                                                                                                                                 | Rejected alternative, and why                                                                                                                                                                                                                                                                                                                                                                                                              |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| A1  | **Membership makes a participant ADDRESSABLE; delegation makes it CONTROLLABLE.** They are two records, and neither implies the other                                                                                                                                                                    | One "participant" record carrying both. That is the conflation the bead is named for. Concretely: under it, adding a worker to a wave would silently confer control over it, and revoking control would silently make it unreachable for the mesh talk the 2026-09-03 topology decision just granted                                                                                                                                       |
| A2  | **The delegation record is 2026-08-15 `D8`'s, adopted verbatim** — `Delegation{controllerSession, childSession, epoch, createdByRunId, effects, state}`, five effects, five states                                                                                                                       | A wave-specific authority record. A second vocabulary for one concept, which AGENTS.md's "look for the existing answer" forbids and which would drift from the workspaces design the first time either moved                                                                                                                                                                                                                               |
| A3  | **The default bundle a coordinator holds over a worker it spawned is `observe + receive-events + send-input + close`, and never `delegate-further`**                                                                                                                                                     | Granting all five. `delegate-further` is exactly the act axis the 2026-09-03 topology decision deferred; granting it by default would adopt transitive revocation by the back door, without the takeover rule that makes it safe                                                                                                                                                                                                           |
| A4  | **The trigger set is closed here**, and it is: human takeover of the pane → `input-suspended`; change of authenticated authority context → `scope-suspended`; participant terminal → `expired`; the controller session ending → `revoked`; an explicit human act → `revoked`                             | Leaving it open, as 2026-08-15 §11.1 did. An open trigger set means every later epic invents one, and the states become decorative                                                                                                                                                                                                                                                                                                         |
| A5  | **The discriminator between the two suspensions is "did the authenticated authority context change", never "did a human touch the PTY"**                                                                                                                                                                 | Revoking on any takeover. 2026-08-15 `D8` already measured the cost: "helping your own worker past a `y/n` prompt would permanently sever the coordinator from it"                                                                                                                                                                                                                                                                         |
| A6  | **Spawn is not a new effect.** `EffectDelegate` already exists in the closed lattice, in the `grant_effects` CHECK constraint, in `contracts/policy.get.schema.json` and in the settings UI as "hand work to another agent". Spawn is a tool ROW with `Effect: []content.Effect{content.EffectDelegate}` | An eighth effect member. It costs eight coordinated edits — a struct field, four list literals, two switches, a SQL CHECK, a schema `required` array and a label — to express something the seventh already expresses                                                                                                                                                                                                                      |
| A7  | **The environment scopes minted into the run fence ARE 2026-08-15 `D9`'s "environments already in scope"**                                                                                                                                                                                               | A separate scope list for spawning. The existing `WithRunScopes`/`narrowRow` (`internal/content/effectpolicy.go:373-431`) already flips a row with no intersection against the fence to `DecisionRefuse` at mint time, "so it is never offered as an executable capability". A second list would be a second enforcement point for one rule                                                                                                |
| A8  | **Two capability types, not one with a role flag**: `*WaveParticipant` and `*WaveCoordinator`                                                                                                                                                                                                            | `*WaveParticipant{isCoordinator bool}`. The type switch at `internal/assistant/kernel.go:1408` is what proves exhaustiveness; a boolean inside one type proves nothing and is one refactor away from being read wrong. `Runner` and `RunWatcher` are already two types for two authorities over the same sessions                                                                                                                          |
| A9  | **Five of the seven wave calls take no resource argument at all.** Mailbox read, inbox check, report, "what does my session hold" and withdraw name the holder's own resources, which live inside the object                                                                                             | Passing a participant id to each and checking it. That is the ambient dispatcher API ADR-0028 decision 4 rejects, and `session.run`'s parameterless schema is the local proof it is avoidable                                                                                                                                                                                                                                              |
| A10 | **`say` and `interrupt` take a HANDLE the object minted, never a raw participant id**                                                                                                                                                                                                                    | A raw id. Delegation is revocable (A2), and a revocable identifier the model can spell from memory survives revocation inside the model's context; a handle the object no longer contains does not                                                                                                                                                                                                                                         |
| A11 | **A participant is addressed as a sub-scope of `ResourceWorkspace`**, whose canonical ids are already hierarchical (`workspace/<id>/tab/<id>/pane/<id>`)                                                                                                                                                 | A ninth `ResourceKind`. The kind set is closed at eight and guarded twice — `validResourceKind` (`internal/content/effectpolicy.go:130`) and the `grant_scopes` CHECK. `ResourceContent` already carries `note/<id>`, `snippet/<id>`, `skill/<id>`, so sub-scoping inside a kind is the established move. **Blocked on `nocx-intbc`**: `internal/agenttools/resourcekind.go` omits `ResourceWorkspace`, so such a row fails assembly today |
| A12 | **Until participant authenticity has a mechanism, a wave call carries NO authority the session does not already have**                                                                                                                                                                                   | Claiming the enrolment pin exists. It does not — see §5 — and a model that assumes "only the enrolled tree may speak for the participant" would be resting on a property `nocx-aqz7o` shows the code does not provide                                                                                                                                                                                                                      |

## 4. Membership and delegation, side by side

This is A1 made concrete, and it is the answer to the bead's title.

|                                 | Membership                                                                            | Delegation                                                     |
| ------------------------------- | ------------------------------------------------------------------------------------- | -------------------------------------------------------------- |
| **What it proves**              | this participant is in this wave                                                      | this controller may currently act on this participant          |
| **What it grants**              | reachability: the mesh talk axis of the 2026-09-03 design — A may address B's mailbox | control: `observe`, `receive-events`, `send-input`, `close`    |
| **Held by**                     | the wave record                                                                       | a controller **session** (2026-08-15 `D8`)                     |
| **Revoked by**                  | leaving the wave, or the wave ending                                                  | A4's trigger set                                               |
| **On human takeover**           | unchanged — a taken-over worker is still in the wave and still reachable              | `input-suspended`: `send-input` suspended, `observe` preserved |
| **On authority-context change** | unchanged                                                                             | `scope-suspended`: no resumption without human approval        |

Two consequences worth stating because they are what the split buys:

**A taken-over worker does not vanish from the wave.** The coordinator keeps seeing it and keeps
being able to send it mail; it loses only the right to type into it. Under the conflated model
the human's takeover would have severed coordination entirely, which is the failure 2026-08-15
`D8` named.

**Revoking control does not mute a peer.** Because the mesh talk axis rides membership, not
delegation, `scope-suspended` on the coordinator's delegation leaves worker-to-worker traffic
intact. The commitments stop; the conversation does not.

## 5. What binds a later request — the honest answer

**This is the question the design set cannot currently answer, and A12 is the consequence.**

2026-08-15 `D13` requires "peer identity, foreground process-tree membership, a non-reusable
pinned handle and human approval". The 2026-08-24 design's `D5` pins to the launcher's pid, which
`exec` preserves. The adversarial review said that is "necessary, and not obviously sufficient".
It is worse than that: **none of it is built, and three of the four candidate carriers are
unavailable in this codebase.**

| Candidate                        | Status                                                     | What defeats it                                                                                                                                                                                                                                                                                                              |
| -------------------------------- | ---------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The per-epoch capability         | Implemented, and it is the ONLY binding today              | `nocx-aqz7o`: it is written in cleartext on the outbound half of the descriptor every descendant inherits (`internal/lifecyclecodec/codec.go:449`; four constructors at `internal/lifecycle/kernel.go:864,875,1039,1050`), whose number is exported as `NOCX_LIFECYCLE_FD` (`internal/shellintegration/launcher.go:129,149`) |
| `SO_PEERCRED` / `LOCAL_PEERCRED` | Implemented, but on the coordinator's socket, not this one | The lifecycle channel is a `socketpair`, so there is no `connect(2)` and no per-peer credential to stamp; remotely it is loopback TCP, which has none by construction. `D13`'s "OS identity from peer credentials" has no carrier here                                                                                       |
| `pidfd` / start-time pin         | Not built                                                  | `grep pidfd` over Go source returns nothing. Start-time is read only in `internal/helper/session/inspect_*.go`, only remotely, and only as a diagnostic — never compared, never pinned                                                                                                                                       |
| Foreground process group         | Implemented, and rejected as an authority                  | ADR-0024 lines 122-128 refuses it by name: "It answers 'who owns tty input now', never 'who wrote these bytes'". **`D13` nonetheless requires it.** That is an unreconciled contradiction between two live documents, and it is not this document's to resolve                                                               |

The wrapper being a shell function rather than an alias, and not exported
(`internal/shellintegration/scripts/nocx.bash:723-728`), stops a descendant enrolling by
**accident**. It stops nothing deliberate, because the function is not the authenticator.

**Therefore A12.** The wave ships with participant authenticity resting on exactly what the
session already rests on, and it says so rather than implying more. Concretely: a wave call may
do nothing a same-uid actor on that machine could not already do through the session itself. The
mechanism is useful at that level — supervision, mail, checkpoints and waking are all still real
— and it does not become an authority boundary until the pin exists.

This is the fail-closed reading of 2026-08-24 `D4`, applied to a property rather than to a
failure: **an authority we cannot enforce is one we do not claim.**

## 6. Spawn, concretely

Per A6 and A7, spawn is additive and needs no new mechanism. What it costs:

1. A `Declaration` row in `internal/agenttools/registry.go`'s `declarations` table — the table
   its own doc calls "the only place a tool comes into existence" — with
   `Effect: []content.Effect{content.EffectDelegate}` and
   `ResourceKinds: []content.ResourceKind{content.ResourceEnvironment}`.
2. A resource resolver mapping the spawn argument to the target environment id.
3. A `Narrow` returning a spawner that holds only the granted environments, beside `narrowRun`
   and `narrowSession` in `internal/agenttools/narrow.go`.
4. Params and result schemas under `contracts/tools/`.
5. **An environment scope minted into the run fence** (`internal/transport/ws_readscreen.go:294-302`),
   which today mints only session, path, content and destination. Without it
   `Registry.ForGrant`'s kind check silently omits the tool and it is never offered.
6. Nothing in the policy matrix, the SQL, the wire contract or the settings UI.

**One behavioural gap, and it is a decision rather than an oversight.** 2026-08-15 `D9` says
reaching beyond the fence "escalates". Today `effectKernel.inScope` returning false produces
`RefusedOutOfScope` (`internal/assistant/kernel.go:568-570`) — a **refusal**, not an **ask**. A
refusal is the safer default and it is what ships until this is designed; making it escalate is a
property of a policy row rather than a special case for one tool, and inventing that mechanism
inside this document would be exactly the "re-deciding a settled question inside something else"
AGENTS.md warns about. Filed rather than smuggled.

## 7. What the topology decision already disposed of

The 2026-08-24 design's open question 10 lists what mesh needs beyond addressable participants:
sender authentication, causal ordering, mutation arbitration, revocation, loop prevention,
dead-letter handling, and "the atomicity hole where a message promises a mutation that then fails
to commit".

The 2026-09-03 talk/act split settles three of the seven without further work:

- **Mutation arbitration does not arise.** Mutations are not distributed across participants;
  they are the coordinator's (that design's `M2`).
- **The atomicity hole is closed by grammar, not by discipline.** A message cannot promise a
  mutation because a mutation is not expressible on the talk channel (`M3`).
- **Revocation is deferred with `delegate-further`** (A3), rather than being owed now.

**Dead-letter handling** has its answer in that design's §5: the per-fact deadline belongs to the
recipient's supervision and escalates to the human. **Sender authentication** is §5 of this
document — it is `nocx-aqz7o` and A12. **Causal ordering** and **loop prevention** remain open and
belong to `nocx-dkawo.4`, where fan-out lives.

## 8. Deliberately out

- **`delegate-further` and transitive revocation** (A3). They arrive with the act axis.
- **The escalate-versus-refuse mechanism** for scope expansion (§6). Filed.
- **Resolving `D13`'s foreground-pgid requirement against ADR-0024's rejection of it** (§5). Two
  live documents disagree; that is its own bead, not a paragraph here.
- **A workspace-level grant source** — ADR-0020's amendment already names `nocx-mp2vd`.
- **Whether reading output is separately granted from control in the general case.** A3 decides
  the wave's default bundle; 2026-08-15 §11 open question 3 asks it for every controller and
  stays open.

## 9. Assertions

1. A participant added to a wave is addressable by every other participant and controllable by
   none of them, until a delegation says otherwise (A1). Asserted both ways.
2. A delegation whose `state` is `input-suspended` refuses `send-input` and permits `observe`
   (A2, A5). This is `ws_run.go:247-255`'s shipped behaviour at a new scope; the test is the same
   shape.
3. A human takeover of a worker's pane leaves the coordinator's mail to that worker working, and
   leaves the worker's membership intact (§4).
4. A change of authenticated authority context moves the delegation to `scope-suspended`, and no
   subsequent delegated request passes under the old context (A4, A5).
5. The controller session ending revokes its delegations; the lineage edges it created remain
   (A4, and `internal/lineage/lineage.go:22-25`).
6. A spawn into an environment outside the run fence is refused, and the refusal names the
   environment (A7, §6). A spawn into one inside it succeeds.
7. A `*WaveParticipant` cannot be handed to a code path expecting a `*WaveCoordinator`, proved by
   the type switch rather than by a test of intent (A8).
8. The mailbox, inbox, report and session-holdings calls have no participant parameter in their
   contract schemas (A9). Asserted against `contracts/tools/`, the way `session.run` already is.
9. A handle for a participant whose delegation was revoked no longer resolves, and the raw id it
   was minted from is not accepted in its place (A10).
10. No wave call permits an action the session's own grant would not (A12). Asserted by minting a
    participant capability from a fenced grant and finding it cannot exceed the fence.

## 10. What would falsify this design

- **A wave record in which membership confers control**, or in which revoking control removes
  reachability. Either collapses A1 and re-creates the defect the bead names.
- **A second authority record beside `Delegation`**, or a wave-specific effect lattice.
- **A `spawn` effect added to the lattice** when `delegate` already carries it.
- **A participant id passed as a tool parameter** where the object could have held it.
- **Any claim that the wave authenticates a participant**, while §5's table stands and
  `nocx-aqz7o` is open. That claim is the one thing here that would be dangerous rather than
  merely wrong.
