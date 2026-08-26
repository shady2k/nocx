# ADR-0019 — One authoritative ledger, disposable projections

- **Status:** Proposed
- **Date:** 2026-08-02
- **Related:** [ADR-0008](0008-command-blocks-as-a-keyboard-first-ledger.md) (blocks as a
  keyboard-first ledger), [ADR-0018](0018-contentdb-engine-and-encryption-at-rest.md)
  (the store this ledger lives in), [ADR-0020](0020-the-agent-gets-a-lane-authority-is-granted-per-run.md)
  (who else writes to it), AD-1 as amended (frontend-derived ledger facts cross the
  control plane), AD-6 (the backend never sniffs the byte stream).
- **Design:** `.internal/specs/2026-07-31-command-blocks-history-syntax-design.md` §2.4,
  §3, §5.2 — this ADR amends §3.1 and §3.2 of that spec.
- **Consulted:** an adversarial review (codex, 2026-08-02) that supplied the
  reconstruction/rehydration/resumption split and the ingest-order argument.

## Context

`command_history` is an interim table and says so in its own comment. Schema v1 —
`environments`, `entries`, `edges`, `artifacts`, `artifact_chunks` — is designed and
tracked as `nocx-rtg0.2`, and the owner's review of it raised three bindings it did not
answer well: host, session, and block. Two of those the spec answers better than the
question implies (a host is an `environment`, a block is an `entry`); the third it refuses
outright, and that refusal is wrong in a way worth writing down.

Two facts about the nearest comparable product shape the decision. Warp keeps **two**
timelines: the shell block flow and the agent conversation — and its documentation states
that commands run inside an agent conversation appear only in that conversation. And Warp's
session restoration writes windows, tabs, panes and recent blocks to its own SQLite
database, **overwriting it on every quit**, so only the last session is restorable.

One fact about ourselves: on 2026-08-02 every recorded command was deleted microseconds
after it was written, because the renderer stamped a presentation clock
(`performance.now()`) into a field the store judged by (`nocx-rtg0.16`). The lesson
generalises past clocks: **what the store judges by, the store must own.**

## Decision

**1. One authoritative ledger. Authorship is a column, not a second store.** A command a
human typed and a command an agent ran are both `entries`, distinguished by `kind`
(`shell | agent | action`) and joined by `caused-by` edges. There is no separate agent
transcript. The question "what happened in this directory yesterday" has exactly one
answer, and it does not depend on who was holding the keyboard.

**2. One ledger does not mean one narrative.** Concurrent human and agent work has only an
arbitrary serialization. The backend-assigned counter is renamed **`ingest_seq`** and means
what it is: the order the writer committed, a stable total order for paging and idempotency
— **not** causality. Anything the product presents as "this happened because of that" comes
from an edge, never from adjacency in the ledger. A UI that reads causation out of
neighbouring rows will lie precisely when two things happen at once, which is exactly when
the user is confused and looking.

**3. "Restore" is three different promises, and they are named separately.**

- **Historical reconstruction** — a query over the ledger: this session's entries by
  `ingest_seq`, with their artifacts. This is what "reopen my tabs and blocks" means, it
  is all we promise first, and it needs no storage of its own.
- **Shell rehydration** — best-effort replay of _explicitly captured declarative_ state:
  cwd, an allow-listed set of environment variables, the connection profile. Never
  implicit, never scraped.
- **Live resumption** — a surviving process substrate. It cannot be synthesized from
  blocks, and nothing in the UI may imply it: a reconstructed block must be visibly not a
  live one, or the product invites you to type into a shell that does not exist.

**4. Projections are permitted and disposable.** The invariant is **not** "no
snapshot-shaped data" — it is **no independently writable snapshot truth**. A table holding
pane layout, viewport position or a restore cursor is legitimate exactly when it is derived
from the ledger and can be deleted and rebuilt with no loss. Recomputing every window from
raw rows forever is a promise that ages badly as presentation rules change; two writable
truths is the failure we are actually avoiding.

**5. Sessions are a restore key and never a recall filter.** §3.1 of the design refused a
`sessions` table because a session id dies with its session. That argument is half right,
and it conflated two questions. As a _recall_ filter a dead session id returns nothing
after a restart, which reads as breakage — so recall stays keyed on `environment` + `cwd`.
As a _restore_ key it is the only thing that can name "that tab", so `sessions` becomes a
real table, owned by a workspace, with `entries.session_id` referencing it and surviving
its deletion.

**6. Derived text is an artifact with provenance, not a string.** Output serialized from
the xterm buffer is a _rendering-derived_ representation — resize and scrollback policy
change what survives — so a capture records its method and version, the terminal dimensions
it saw, which end was truncated and where the gaps are, and derived text links to the
capture it came from. A claim (an agent's summary, a recall ranking) links to the artifact
it read. Evidence, not adjacency.

**7. Reconstruction states its own horizon.** Retention will eventually evict artifacts a
reopenable session still references. Two rules: a session keeps a **compact manifest** after
its heavy artifacts expire, and the reconstruction shows the gap rather than rendering a
shorter history as if it were the whole one. A silent truncation is how a memory product
teaches you not to trust it.

## Rationale

The single-ledger bet is the whole product thesis. A terminal that remembers is worth
building only if its memory is complete; two timelines mean two incomplete answers, and the
one that matters — "what actually ran here" — is in neither. Warp's split is a reasonable
consequence of adding agents to a shipped terminal; we do not have that constraint.

Restore-as-a-query removes a subsystem rather than adding one, and it is strictly more
capable: a session from three days ago reopens exactly as cheaply as the last one, because
there is nothing special about "the last one". The three-way naming exists because the
single word "restore" is what lets a product ship a screenshot and call it a session.

Renaming `seq` to `ingest_seq` costs one identifier now and prevents a class of confident
wrong answers later, when the ledger is being asked _why_ rather than _what_.

## Consequences

- Schema v1 gains `sessions` (and, per ADR-0020, `workspaces`); `entries.session_id`
  becomes a real reference with `ON DELETE SET NULL` — an entry outlives its session.
- Every artifact carries capture provenance. This is more columns than a `body BLOB`, and
  it is the difference between "the output was 4 KiB" and "the output was 4 KiB of a
  rendering, taken this way, missing this end".
- The reconstruction path and the recall path are the same read path, and a change to
  either is a change to both — deliberately.
- The UI owes a visible distinction between a reconstructed block and a live one, and an
  honest marker where retention has eaten the middle.

## Alternatives considered

**A separate snapshot store (Warp's shape).** Rejected: a second writable truth, an
overwrite-on-quit lifecycle that makes only the last session restorable, and no path from
"restore" to "search" even though both read the same events.

**A separate agent transcript.** Rejected on the thesis above. Kept as a _projection_: a
conversation view is a filter over `kind='agent'` and the tree beneath each turn, which is
cheap and throws nothing away.

> **Amended 2026-08-23 by [ADR-0040](0040-a-block-is-a-node-in-an-ordered-tree.md).** This
> line named a `conversation_id` column, which is dropped: it was threaded from a
> `SubmitEntry` field no caller ever set, so every row carried NULL and no projection could
> ever have used it. What replaces it is stronger than what was described — a turn's prose,
> its tool calls and the commands it ran are its CHILDREN, in order, so the conversation
> view reads a turn whole rather than filtering a flat table for rows that might belong
> together.

**Keeping the session out of the schema entirely (the design as written).** Rejected: it
makes "reopen that tab" unimplementable without inventing a second identity for the same
thing.

## Not decided here

Full-text search and its eviction coupling; AI dialogues as a data class with their own
retention, sensitivity propagation and possibly their own key; the effect taxonomy and the
autonomy policy (ADR-0020); the eviction algorithm itself.

## Revisit when

Reconstruction of a large session is measurably slow enough that a materialized projection
stops being an optimisation and becomes the thing the UI depends on — at which point rule 4
is what keeps it honest, and it must be provably rebuildable from the ledger in a test, not
in a comment.
