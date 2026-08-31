# Restart reconciliation: what the content store may assume once a session outlives its backend

## 0. Why this document exists

The execution-host design (D1a) promises that a coordinator replacement does not destroy a
session or the ability to reattach to it. **The content store's startup path contradicts that
promise today, and it does so deliberately and correctly for the world it was written in.**

`internal/content/sqlite.go`'s `Open` runs two sweeps:

- **`closeOpenEntries`** (`:365`) closes **every** entry that never reached `closed` — status
  `unknown`, or `interrupted` for an ask — and marks **every** non-terminal execution
  `interrupted`, in one transaction.
- **`dropDeadSessions`** (`:410`) executes `DELETE FROM sessions` and `DELETE FROM
session_output`.

Each is justified by a premise stated in the code: "a session is server-authoritative (AD-7),
lives inside one backend process and **cannot outlive it**", and "a recording is the bytes ONE
pipe produced, **the pipe cannot outlive the process**, so at open no recording names anything
live".

**This design removes both premises.** Left as it is, a replacing coordinator would, at exactly
the moment D1a exists to protect: delete the `sessions` row of a running session, null the
`session_id` of every entry that names it, delete its recording and every chunk by cascade, and
close its still-open entry as `unknown` — declaring finished a command that is still running.

Nothing here is a defect in the current code. It is a theorem whose axiom is being repealed.

## 1. What this crosses

**AD-7** — session identity is server-authoritative. The execution-host design's D10 amends it:
the execution host mints session identity and the durable handle names its generation. This
document is where that amendment reaches the store, because "server-authoritative" was the
reason `Open` could treat every row as dead.

**ADR-0019 — one authoritative ledger, disposable projections.** Untouched: no second store, no
second ordering, `ingest_seq` unchanged. What changes is _when_ a row may be judged dead.

**ADR-0043 — one connection to the encrypted store.** Binding, and it shapes the answer:
reconciliation may not open a second connection, and it may not run inside `Open`, because at
`Open` there is no carrier to ask (D2).

**The schema's two edges** (`sqlite.go:834`), which already say exactly what survives what:

- `entries.pane_id` is **the ANCHOR** — "durable, frontend-minted, and what makes restore
  possible", `ON DELETE SET NULL`.
- `entries.session_id` is **PROVENANCE** — "which pipe it ran in, null once that pipe is gone",
  `ON DELETE SET NULL`.

Neither definition changes. Only the moment at which a pipe counts as gone.

**`SessionOutputRecording`** (`session_output.go:130`) already has the shape a hole needs:
`Runs` is a list precisely because "the cap drops the middle: head and tail are two runs with a
hole between them"; `Gaps` says what is missing "in the ledger's own Gap shape"; and `Produced`
is "how many bytes the session has produced in total, **including what was dropped** — the
recording's end offset. **It is what makes a hole measurable rather than invisible.**" D5 uses
that, rather than inventing a parallel notion.

**The execution-host design's D8** makes a gap a fact about the stream rather than a caller
defect, which `session_output.go:70` currently calls it in as many words.

## 2. Decisions

### D1 — There are three classes now, and the third is the whole problem

At `Open`, a `sessions` row was in exactly one class: dead. It is now in one of three:

| class       | meaning                                                          |
| ----------- | ---------------------------------------------------------------- |
| **live**    | a reachable generation reports this session                      |
| **absent**  | a reachable generation was asked and does not report it          |
| **unknown** | nobody could be asked — host unreachable, credential vault-gated |

**`unknown` is not a transient inconvenience; it is a durable state a user can sit in for
hours.** A laptop opened away from the network, a host behind a jump box that is down, a
vault-gated credential waiting on a person (execution-host D16's third row) — all produce rows
that are neither live nor provably dead, for as long as that lasts.

Every decision below follows from refusing to collapse `unknown` into `absent`. Collapsing it is
what the current code does, and it is correct only while the collapse is a theorem.

### D2 — The sweep leaves `Open`, because `Open` cannot ask

`Open` runs before any carrier exists. Asking a remote host needs SSH; SSH may need the vault;
the vault needs the store. **The question cannot be answered where it is currently asked**, and
answering it wrongly is what deletes live work.

So `Open` stops judging. It performs no deletion and no closure. What it does instead is
**mark**: every `sessions` row from a previous incarnation, and every entry and execution left
open, is stamped `unreconciled` with the incarnation that is now starting.

Reconciliation runs afterwards, on the ordinary connection, when the coordinator has carriers —
one connection throughout, so ADR-0043 is untouched.

**Rejected: keep the sweep at `Open` and ask only about local sessions.** It would split the rule
by locality, so a remote session's row would be judged by a different mechanism from a local
one's, and the two would drift. The execution-host design's D11 spent its argument on exactly
this: one shape locally and remotely.

**Rejected: leave the rows silently untouched and let them age out.** A row that is neither
swept nor marked is indistinguishable from a live one, and the surfaces that read it would show
a running session that has not existed since yesterday.

### D3 — Reconciliation is per session, per verdict, and idempotent

For each `unreconciled` session, in whatever order carriers become available:

- **live** → clear the mark. The row stays, its entries keep their `session_id`, its recording
  keeps its bytes, and its open entry stays open, because the command is still running.
- **absent** → apply exactly what `Open` used to do, for that session alone: delete the row
  (nulling `session_id` through the existing foreign key), delete the recording, and close its
  open entries as `unknown` — `interrupted` for an ask. **One transaction per session**, which is
  what preserves `closeOpenEntries`'s reason for being one transaction today: "the interval they
  guard has one closing event", and a reader must never see one half of it.
- **unknown** → nothing. The mark stays and the row stays. It is reconciled on a later attempt.

Reconciliation is **idempotent and resumable**: it may run again at any time, and an interrupted
pass leaves rows marked rather than half-judged.

**A session may never be reconciled `absent` on the strength of a failure.** A refused
connection, a timeout, a sealed vault, an unreachable host — every one of them is `unknown`.
`absent` requires a reachable generation that was **asked** and answered no. This is D9 of the
lifecycle design applied to rows instead of directories: liveness is a fact obtained, never an
inference from an error.

### D4 — An entry's verdict follows its session's, and its anchor never does

`closeOpenEntries` becomes reconciliation-driven, and the schema already says how far the
consequence may reach: `session_id` is provenance and is nulled; `pane_id` is the anchor and is
**untouched in every branch**. A block whose session is gone is still a command that ran, in the
pane it ran in.

The one genuinely new rule: **an open entry whose session is live stays open.** Today every open
entry closes, because every session was dead. Closing an entry whose command is still producing
output would put a finished block above a running process — the product contradicting itself in
the most visible place it has.

An `unreconciled` entry is shown as such (D7). It is not shown as running, and it is not shown as
finished.

### D5 — A never-ingested hole is a gap with its own reason, and `Produced` already measures it

Two kinds of hole exist once a recorder can be absent, and they must not be told to the user in
the same words:

- **evicted** — ingested, then dropped for the per-command cap. Already representable; already
  reported with `Truncated` reason `cap`.
- **never ingested** — the coordinator was not there. **Not representable today**: `Append`
  refuses any discontinuity (`session_output_sqlite.go:141`), and `session_output.go:70` calls a
  gap "a caller defect and not a fact about the stream".

So `SessionOutputRepository` gains one operation — `Skip(sessionID, resumeAt, reason)` — which
advances the recording's produced cursor across a range that was never offered, records the gap
in the same `Gap` shape the cap uses, and **leaves the recording appendable afterwards**. That
last clause is the point: `ws_session_record.go:106` stops recording permanently once it loses
its place, which would turn one missed second into a lost session.

**And the reason is a second value, not `cap`.** Telling a user the cap dropped bytes that were
never ingested is a false statement in the product, and the existing `Truncation` carries a
reason precisely so it can be exact.

`session_output.go:70`'s sentence is amended rather than contradicted: a gap is a caller defect
**when the caller had the bytes**, and a fact about the stream when nobody did.

### D6 — Deleting dead recordings was the only bound on them, and the bound must be replaced

`session_output.go:157` says why these rows are outside the budget sweep: "its unit is
`artifacts.byte_len` ordered by `ingest_seq`, and a session recording has neither", and it names
the two bounds it has instead — "the bound on a live recording is the per-command cap, and the
bound on a dead one is **this line**", meaning `dropDeadSessions`.

**D2 removes that line. So the bound is gone, and a replacement is owed here rather than
discovered later.** A recording whose session is reconciled `absent` is deleted at that moment
(D3), which restores the bound for the ordinary case. What is new is the `unknown` case: rows
that are never reconciled because the host never comes back.

The bound on those is a **retention age on the mark**: an `unreconciled` recording older than the
configured age is deleted, and its session row with it, without being judged `absent` — the
product says the host was never reachable again, not that the session ended. Deleting a recording
is not deleting a block; the entry keeps its anchor.

### D7 — What the product shows while a row is `unreconciled`

An `unreconciled` session is shown as **neither running nor finished**, in those words. History
and the block ledger show its entries with their state pending, and the reason — "this host has
not been reachable since the app restarted", or "waiting for the vault to be unlocked" — because
`unknown` has causes and the user can act on some of them.

This is AGENTS.md's soft-degrade rule at its sharpest: the surface that would otherwise show a
confident `unknown` is showing a verdict nobody has reached.

## 3. Assertions

1. **A live session survives `Open`.** With a session running on a reachable generation, a fresh
   coordinator's `Open` deletes no `sessions` row, deletes no recording, nulls no `session_id`
   and closes no entry — asserted against the real `Open`, which deletes all four today.
2. **Its open entry stays open** and is not stamped `unknown` while its process is still running.
3. **An absent session is reconciled exactly as `Open` used to sweep it**: row gone,
   `session_id` nulled, recording gone, open entries `unknown` (or `interrupted` for an ask), in
   **one transaction** — a reader never observes the entry closed while the execution is not, or
   the reverse.
4. **`pane_id` survives every branch**: live, absent and unknown alike leave the anchor intact,
   so the block is still restorable into its pane.
5. **A failure is never a verdict.** A refused connection, a timeout, a sealed vault and an
   unreachable host each leave the session `unreconciled`; none produces `absent`. Asserted per
   failure mode, not once.
6. **Reconciliation is idempotent and resumable**: killed at each step in turn, the next pass
   completes it and no row is half-judged.
7. **One connection throughout** — reconciliation opens none of its own (ADR-0043).
8. **`Skip` records a gap and the recording survives it**: after a skip, `Produced` has advanced
   past the range, the gap is present in the recording's `Gaps`, and **subsequent appends
   succeed**.
9. **The gap's reason distinguishes the two kinds**: a cap eviction and a never-ingested range
   are not reported with the same reason, and the product's wording differs.
10. **The replacement bound binds**: an `unreconciled` recording past its retention age is
    removed, and doing so does not delete its entry or its anchor.
11. **The absent path restores the old bound**: a host that comes back and reports a session
    absent leaves no recording behind.
12. **The product shows a third state.** An `unreconciled` entry renders as neither running nor
    finished, with its reason, and does not render as `unknown`.
13. **And the paired positive**: on an ordinary machine with a reachable host, a session survives
    a coordinator replacement, its recording is continuous across the replacement where the
    coordinator was reachable throughout and carries an explicitly-reasoned gap where it was
    not, and its block is restorable into its pane in both cases.

## 4. Deliberately out of scope

- **Which sessions exist at all** — that is the lifecycle design's discovery (its D7/D8). This
  document consumes a verdict; it does not obtain one.
- **The retention age's number.** It is a setting with a default, and choosing the default is
  ordinary product work; what this document owes is that the bound exists at all (D6).
- **Multi-coordinator reconciliation.** With two coordinators attached to one session, whose
  reconciliation wins is document 2's, together with the ledger it already owns.
- **Anything about the ledger's ordering.** `ingest_seq` and idempotency are untouched.
