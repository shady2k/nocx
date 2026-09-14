# ADR-0067 — A write into a descendant's pane is one step under a helper-minted target

- **Status:** Accepted
- **Date:** 2026-09-14
- **Supersedes:** [ADR-0064](0064-a-pane-that-is-read-may-be-answered.md) §1 (the closed key
  set, named by option text, re-read immediately before the write) and
  [ADR-0029](0029-a-keystroke-is-bound-to-what-makes-it-meaningful.md) (Proposed — a keystroke
  bound to what makes it meaningful, including its rule 5's model-authored condition).
  **Extends** ADR-0064 §2 (the pane's reading may reach the session that holds it) from a
  directly held participant to every descendant reached through the delegation chain.
  **Restates** ADR-0064 §4 (what a menu answer may never do) where it survives. Neither
  ADR-0064 nor ADR-0029 is edited; this is a new record.
- **Related:** builds on [ADR-0066](0066-one-emulator-and-it-is-the-backends.md) (the one
  emulator moved into a session runtime beside the PTY, in the helper) and
  [ADR-0063](0063-typing-is-refused-on-evidence-against-the-rule.md) (typing is refused on
  evidence against the rule, kept here for agent targets). Docs/architecture.md's AD-6 "two
  powers" bullet chain is amended to cite this record. Epic `nocx-6q1uh`.
- **Design:** `.internal/specs/2026-09-14-the-session-surface-design.md`, revision 6
  (`22377b6b`), §2–§9, §11.

## Context

ADR-0064 §1 closed the set of keys a write into an enrolled pane could carry to exactly what
a positively identified menu itself offers — move the selection, confirm it, dismiss it —
named by the option's own text, with the frame **re-read immediately before the write** so
the identification authorising the write was "the one taken microseconds before it, never
the one a caller saw." That re-read was cheap to state as a rule because the party reading
the frame and the party writing the keystroke were the same process: `internal/agenttyping`
held the grid and the PTY write path together (`docs/architecture.md:165`).

ADR-0029, filed the same season and never adopted (`Proposed`), reached for the same
guarantee from the other side: a keystroke is meaningless without the frame it was computed
against, so a proposal must be bound to what makes it meaningful rather than to raw frame
identity. Its rule 1 kept the final gate local and synchronous — no round trip, because a
round trip has a gap and the gap is another repaint. Its rule 4 answered the common case with
a scoped diff. Its rule 5 went further: where the diff touched the scope, **the model
authors the condition** the gate then evaluates at delivery. That last rule needed a
condition language nobody had built, evaluated against a screen the model had already
stopped seeing by the time it was checked.

**Both closed-form rules stopped holding when the emulator moved.** ADR-0066 puts the one VT
emulator in a long-lived session runtime beside the PTY — in the helper, for both a local
session and a remote one carried over SSH — and makes it the runtime's, not the coordinating
process's. A coordinator no longer holds the grid it would need to re-read a microsecond
before writing: the frame and the write are now separated by an RPC hop, and "re-read
immediately before the write" is not a sentence a remote caller can execute atomically.
What the coordinator can still do is read a **snapshot** the helper hands it, classify it,
and ask the **same helper** to mint something the caller then has one shot to spend — which
moves the re-read from "the caller does it just before writing" to "the party that owns the
PTY validates it at the moment it is about to write," i.e. the session I/O owner's commit
point (§5.3). That owner already has to exist for an unrelated reason: the runtime mutex a
program can hold across a PTY write stalls the pump that must also ingest that program's
output (§5.1, `nocx-6q1uh.1`), and a mutex cannot give both an uncontested drain and a
linearised, validated write — an owner goroutine can, and once it exists it is the only place
left where "read the current frame, then write" is one step.

With the owner holding both the read and the write, the closed-key-set rule and the
model-authored-condition rule are no longer the cheapest way to say "meaning survived until
the write": a token minted from the exact snapshot the coordinator classified, carrying a
structural digest of the rows and cursor that snapshot named, and consumed exactly once at
the commit point, says it directly and needs no key list and no condition language to do so.

## Decision

**One state-changing step per helper-minted, one-shot target.** `session.read` asks the
helper for a retained snapshot; the coordinator classifies it and asks the same helper to
mint a **target** — `menu`, `input`, `working` or `region` — carrying a structural digest of
the rows, cursor and geometry that snapshot named (§6.1–6.2). Exactly one of a key, one text
atom written as a paste, or a menu option's own text as drawn authorises **one** write under
that target (§4.2); a sequence is separate calls with fresh targets. The target is bound to
one delegation and one capability (`forged` under another); it is HMAC-signed, expires at 60
seconds, and is consumed atomically — a second submission of the same canonical intent
replays the recorded result, a different one is `token_spent` (§6.2). This supersedes
ADR-0064 §1's closed key set: the set is no longer closed by enumerating what a menu offers,
because the token already names the exact frame the key is meaningful against, and a target
that does not match the frame at commit is refused (`incomparable`) regardless of which key
was sent.

**The condition is validated at the session I/O owner's commit point (owner decision 4,
2026-09-14), not by a coordinator-side re-read and not by a classifier moved into the
helper.** The owner that already serialises drain, ingest and write for a session (§5.2–5.3)
is the only party positioned to compare the token's digest against the frame at the instant
before the write, because it is the only party that can make "compare, then write" one
uninterruptible step. This is what ADR-0029's rules 1 and 4 were reaching for — a local,
synchronous, scope-bound check with no round trip — realised as the target mechanism instead
of as a condition the caller or the model would have to author. ADR-0029's rule 5 is not
adopted: **a changed screen is a refusal returned to the caller, which re-reads and decides**
(owner decision 3, 2026-09-14) — there is no persisted, model-authored predicate for the gate
to evaluate, and no second round trip to author one. A caller that still wants the step reads
again, classifies again, and mints a fresh target.

**SSH-channel sessions refuse conditional input.** A local helper holding a remote shell's
channel has no readiness boundary on it — a blocking reader may hold bytes the owner has not
yet seen — so there is no read barrier to validate a token against, and `session.keys` /
`session.message` on such a session are refused `no_read_barrier`; reads and snapshots still
work (§5.2). A descendant reached over SSH is out of this epic's reach in any case
(`nocx-cxq7d`); the refusal is what makes that explicit rather than silently degrading.

**An orchestrated agent acts on the panes of its descendants and only talks to its
neighbours** (owner decision 7, 2026-09-14). `DescendantPaneAccess` is bound once per caller,
before dispatch, and resolves server-side through the delegation chain — `Delegation.
ControllerSession` followed upward, each link `Active` (§7.1) — so it reaches every
descendant transitively, not only a directly held participant. This **extends ADR-0064 §2**,
which crossed a pane's reading only to "the session that holds the participant": that reach
now follows the chain rather than stopping at one hop. A plain shell and a neighbour pane are
never reachable this way — talk between peers stays the mesh `nocx-i8umd` already gives them,
and act stays the star this record describes; a `session.read` naming a session that is not a
descendant is refused `not_reachable`.

**Revocation is confirmed two ways, and either one is enough.** Ending or suspending a
delegation bumps its generation under the workers store's mutex and, in the same call, sends
`session.access.bump` to every helper holding a descendant pane under it; a revocation is
confirmed either by that bump being **acknowledged** — which the owner does only after every
older uncommitted intent on that session has reached a terminal `access_revoked` — or, if the
helper never acknowledges, by the **monotonic `commitBy` deadline** carried on every intent
having passed, after which no intent minted before the bump can still commit (§7.2). Neither
path depends on the connection or the helper answering; the deadline is what makes the
barrier provable without an answer.

**The renderer screen path stays for the assistant's own pane, for now.** The built-in
assistant reading the pane of its own run keeps going through the existing renderer path
(`internal/assistant/blocks.go`, `internal/transport/ws_readscreen.go`) rather than through
the helper snapshot this record defines, because that path reads a running command block's
region and block boundaries still live in the renderer until `nocx-2v80t` gives the backend
the blocks — the helper's frame has no block to cut against. A descendant's pane is read from
the helper only. Retiring the renderer path belongs to `nocx-2v80t`, or to `nocx-3g262` once
the assistant reads arbitrary panes (found while planning, 2026-09-14).

## Rationale

**A token bound to a structural digest of the exact frame is what "meaning survived" was
always asking for**, and it needs neither a closed key list nor a condition language to say
it: any key, text atom or option is safe under it precisely because the target refuses to
match a frame it was not minted from, which is a stricter and cheaper guarantee than
enumerating "what a menu offers" ever was. ADR-0064's own list existed because nothing kept
the key list closed except naming it; a digest keeps it closed by construction.

**Alternatives the design considered and rejected, each named because it looked like the
obvious fix:**

- **Coordinator-side re-read**, i.e. keep ADR-0064's mechanism and have the coordinator fetch
  a fresh frame immediately before sending the write. Rejected: with the emulator on the
  helper (ADR-0066) the coordinator's "immediately before" is a round trip away from the
  write, so the frame it re-reads can already be stale by the time its write lands — the
  exact gap ADR-0029 rule 1 named and refused to accept a round trip across. Only the party
  that performs the write can make the check and the write one step.
- **A classifier moved into the helper**, so the helper itself decides whether a key still
  means what it meant. Rejected: that is a second implementation of the agent rule engine,
  now living beside the PTY instead of in the coordinator that already owns it (AD-8's "one
  owner per behaviour"), and it does not remove the need for a digest-bound target — it only
  relocates the classification without changing what has to be validated at commit.
- **A mutex shared with the pump** (the incremental fix to `nocx-6q1uh.1` that this record's
  target mechanism rides on). Rejected in §5.1: a program that floods output while a write is
  pending needs the pump to keep draining without waiting on that write, and a mutex cannot
  give an uncontested drain and a linearised validated write at once — only a single owner
  goroutine that queues both can.
- **A model-authored condition language** (ADR-0029 rule 5). Rejected outright rather than
  adopted alongside the target mechanism: it needs an evaluator nobody has built, it is
  authored blind to what will actually move, and owner decision 3 answers the case it existed
  for more simply — a changed screen is just a refusal, returned to the caller to re-read and
  re-decide, with no persisted predicate in between.

## Consequences

- **`docs/architecture.md`'s AD-6 "two powers" bullet chain gains an amendment** citing this
  record: power (1) is restated as conditional input under a helper-minted, one-shot target
  over a descendant's pane, including input during a turn; the re-read becomes validation at
  the session I/O owner's commit point.
- **ADR-0064's own row moves to `Accepted (§1 superseded by ADR-0067)`**; its §2, §3 and §4
  are untouched as records — §2 is extended here rather than edited there, and §4's
  prohibitions are restated below because an amendment is where a reader looks for the
  limits, not because §4 itself changed:
  - a write moves **no** wave state, lifecycle attempt or execution attempt;
  - nocx **never** chooses an option itself, not on a timeout and not to unblock a spawn;
  - a pane's own agent may not answer its own menu — the caller is always a session holding a
    delegation over it, never the participant itself;
  - the grid reaches **no** network destination of nocx's own, and no session outside the
    caller's descendants is read or written at all.
- **ADR-0029's row moves to `Superseded by ADR-0067`.** It never shipped (`Proposed`
  throughout), so nothing built against its condition language needs to change; what it
  reserved — "the condition language," "whether program profiles supply canned conditions" —
  is answered by not needing one.
- **The built-in assistant acting on arbitrary panes is NOT decided here.** That surface —
  an outer capability over every pane in every workspace, a program predicate over a
  foreground process group, the echo floor, approval of asynchronous text — is epic
  `nocx-3g262`, which inherits owner decisions 1–3, 6, 8 and the review findings that belong
  to it (design §14). This record's target mechanism, commit-point validation and revocation
  scheme are available to that epic to build on; its widened reach and its process-identity
  problem are that epic's to solve.
- **`internal/agenttyping`'s `ReadMenu` and `Typist.Choose`, and the `owedTasks` /
  `withOwedTask` machinery, are retired by the implementation this record authorises** — named
  here because a later reader asking "why is this ADR's mechanism not wired the way ADR-0064
  described" should find the answer is this record, not a drift nobody wrote down.
