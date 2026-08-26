# ADR-0040 — A block is a node in an ordered tree, and everything drawn is a block

- **Status:** Accepted
- **Date:** 2026-08-23
- **Related:** [ADR-0008](0008-command-blocks-as-a-keyboard-first-ledger.md) (the block IS
  the domain object — the ledger is made of blocks, not of a general intent model),
  [ADR-0019](0019-one-authoritative-ledger-disposable-projections.md) (one authoritative
  ledger, disposable projections; §6 derived text is an artifact with provenance; §7
  retention evicts bodies and leaves entries),
  [ADR-0020](0020-the-agent-gets-a-lane-authority-is-granted-per-run.md) (decision 4: a
  retry after approval is an execution of the same intent, never a new intent — which is
  why `executions` survives this ADR unchanged),
  [ADR-0039](0039-an-assistant-turn-is-one-entry.md) (**amended by this ADR**: the half
  that says a turn owns its answer as one artifact),
  AD-8 (one owner per behaviour), `nocx-shxv0`, `nocx-9sqii`, `nocx-lfxi6`.

## Context

A turn draws more than one thing. The model writes prose, calls a tool, writes more prose,
runs a command, concludes. On screen that is a sequence, and the sequence is the point: a
sentence written _before_ a command explains why the command was run, and a sentence
written after it is a conclusion drawn from its output. Vertical position in a terminal is
a claim about time.

The store did not have that sequence. A turn was **one entry** whose answer was **one
artifact** (ADR-0039), and the things it caused were separate entries joined by a
`caused-by` edge. So the store held the answer whole and the causes apart, and the
renderer had to put them back together.

Three attempts to do that, each paid for:

**`nocx-shxv0` — a call is a line in the answer's flow.** A tool call left no trace at
all, so it became a one-line note inside the answer body, drawn where it arrived. What it
could show was the tool and the _derived resource_, on the argument that the resource is
the readable half. It is — when it differs between calls. For every session-scoped tool it
does not: `readScreen`, `blocks.list` and `blocks.read` all name the pane. One turn drew
four calls as the same three words, two of them `blocks.read` of different finished
commands, and the person reading it could not tell what had been read.

**`nocx-9sqii` — the anchor.** A command the turn ran had to stand between the prose
before it and the prose written from it, so the causal edge grew an `at`: how many UTF-16
units of the answer had been written when the call happened. The answer was then CUT at
those offsets and the turn drawn as N top-level blocks, each repeating the question in its
header under a `continued` badge. The offset is the whole tell: it exists only because the
unit that is DRAWN (a run of prose) and the unit that is STORED (the whole answer)
disagreed, and something had to translate between them.

**`nocx-lfxi6` — one block carrying children.** The fragments became children of one turn
block, which fixed the repeated question and gave a call a real block with its arguments
in the header. But with the answer still one artifact, all the prose landed below all the
children — so a turn that said «смотрю, что там:» printed that sentence _after_ the output
of the command it was announcing. A fixed arrangement lies whenever the model interleaves.

Every one of those is the same defect in a different place: **two representations of one
order, and a translation between them.** AGENTS.md names the shape — two answers to one
question agree everywhere anyone looks and disagree somewhere nobody did.

## Decision

**Everything drawn in the scrollback is an entry, and entries form one ordered tree.**

1. **`entries` gains `parent_id` and `pos`.** `parent_id` references another entry; NULL is
   a top-level block. `pos` orders siblings, `UNIQUE (parent_id, pos)`. Top-level order
   stays `ingest_seq`.

2. **`entries.kind` gains `text`** — one run of assistant prose. It has no execution: it
   was printed, not attempted. Its shape is declared by a CHECK rather than left to
   convention (see Consequences).

3. **An artifact belongs to its BLOCK.** `artifacts.entry_id` becomes the owner and
   `execution_id` becomes optional provenance — _which attempt produced this body_, when
   there was an attempt. A `text` block has a body and no attempt; a command block has
   both; a turn now has neither, because its prose moved into its children.

4. **The `caused-by` edge is retired**, and with it the `{pos, at}` payload. Containment is
   a column, so the database guarantees one parent, which an edge never did. `edges` keeps
   the relations that are genuinely not a tree: `rerun-of`, `supersedes`, `cites`,
   `in-span`, `references`.

5. **`at` is deleted** — from the wire, the contracts and the store. There is no text to
   cut: a run of prose is a block, in its own place in the order.

6. **`conversation_id` and `reviewed_at` are dropped.** Neither has ever been written with
   a value; `conversation_id` is threaded from a `SubmitEntry` field no caller sets, and
   `reviewed_at` is scanned into a struct field nobody reads.

`executions` and `artifacts` keep their jobs. An execution is an ATTEMPT, and there are
several per entry by design — ADR-0020 decision 4 makes the approved retry attempt 2 of the
same intent. An artifact is a BODY WITH PROVENANCE and its own retention horizon; ADR-0019
§7 evicts bodies and leaves the entries, which is only possible while the two are separate
rows.

A turn is therefore:

```
block  turn-1  kind=agent   intent="what can I clean up?"   → execution (the run)
  ├ block t-1   kind=text    pos=0                          → artifact (prose)
  ├ block sh-1  kind=shell   pos=1  intent="du -sh …"       → execution → artifact (VT)
  ├ block t-2   kind=text    pos=2                          → artifact (prose)
  ├ block act-1 kind=action  pos=3  intent="blocks.read"
  └ block t-3   kind=text    pos=4                          → artifact (prose)
```

One query, one sort, drawn top to bottom. The live path and the restore draw the same list
rather than two projections of one string, so they cannot disagree — which is the criterion
`nocx-9sqii` could only meet by keeping two cutters in step.

## Consequences

**ADR-0039 is amended, and half of it survives.** "A turn is one entry" stays true of its
IDENTITY: one `entryId` routes the deltas, carries the grant, and is what copy and the
conversation address. What is dropped is the other half — that the turn's own body is the
answer. The turn's body is now its children. ADR-0039's own reasoning is what argues for
this: it retired a second entry because that entry "was only ever an ADDRESS for the
deltas". The same test applied to the anchor retires the anchor.

**`text` declares its shape rather than leaving columns to convention.** The objection to
putting prose in `entries` is real: the table is built around intent → attempt → outcome,
and a run of prose has no intent, no attempt and no meaningful status. Left implicit that
becomes "for `text` this column is NULL and that one does not apply", which is how a table
rots. So it is enforced:

```sql
CHECK (kind <> 'text' OR (
         parent_id IS NOT NULL AND pos IS NOT NULL AND
         intent = '' AND phase = 'closed' AND status = 'success'))
```

A `text` block is always somebody's child, is born closed and successful — printing text can
neither run nor fail — and names no intent, because the intent was the question.

**Why `entries` is the right table for it.** ADR-0008 decided that the block IS this
product's domain object: the scrollback is the ledger, not a view onto some more general
model of intents. Under that reading a run of prose is an ordinary block — one that
happens to have no attempt — and the alternative reading, where `entries` means "intents"
and the UI composes them, is what forces a second ordering structure to exist somewhere
else. We are choosing which of the two `entries` means, and ADR-0008 already chose.

**Streaming addresses the piece.** `agent.runDelta` carries the block its text appends to.
`seq` stays monotonic per run, so replay after a reconnect is unchanged. The server opens a
`text` block on the first delta after a call and closes it when the next call arrives — the
boundary is the backend's, never the renderer's.

**Retention gets a rule it did not need before.** A partially evicted answer — pieces 1, 3
and 7 surviving — would read as a complete answer that is missing its middle, which is
worse than an answer that is plainly gone. So the prose of one run is retained or evicted
**as a unit**, and the unit is the run. This is a policy on top of ADR-0019 §7, not a
change to it: §7 still evicts bodies and leaves entries, and a turn whose prose has been
evicted keeps every block and says the text is gone.

**The conversation is assembled from the children**, in `pos` order, per run. That is
`nocx-0s2gh.2`'s direction anyway. Because the prose blocks hang off the run through their
parent, a second attempt has its own set and the attempts cannot silently interleave.

### Rejected

**An attributes/EAV table** (`entry_id, name, payload`), to shrink `entries`. It trades
enforced facts for unchecked strings: `kind`, `phase`, `status` and `sensitivity` are closed
enums the database checks, the table is `STRICT` so `duration_ms` is genuinely an integer,
and `capture_key` carries a unique index. None of that survives a name/value row. The
sparse extension already exists and is the right size — the `payload` column, "kind payload,
sparse extension only": a column is what every kind has and the database must check, and
`payload` is what one kind has.

**A per-execution event tape** (a fourth table listing children and byte spans of one
canonical answer artifact, in order). It is coherent and it keeps `entries` meaning
"intents" — but it does not remove the offsets, it relocates them: the spans are the anchor
under another name. It also adds a table to express an order that a parent column and a
position express with none.

**`parent_id` referencing the EXECUTION rather than the entry.** It is more precise —
strictly, a child was caused by one attempt — and it was rejected as precision we have no
use for. Nothing today re-runs a turn; when something does, `rerun-of` already relates the
two. A second mechanism for the same fact is what this ADR exists to remove.

**Storing prose as several artifacts of one execution, ordered by a position, and merging
them with the causes on read.** It needs no new table and no new kind, and it was rejected
because the order of one turn would live in two tables and be assembled by a join on a
shared counter. The list is one list; it belongs in one place.
