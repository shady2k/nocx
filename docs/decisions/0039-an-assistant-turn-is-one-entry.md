# ADR-0039 — An assistant turn is one ledger entry: the question is its intent, the answer is its body

- **Status:** Accepted
- **Date:** 2026-08-23
- **Related:** [ADR-0019](0019-one-authoritative-ledger-disposable-projections.md) (the one ledger; §6 derived
  text is an artifact with provenance, §7 retention evicts bodies and leaves entries),
  the AI-assistant surface design
  (`.internal/specs/2026-08-13-ai-assistant-surface-design.md`, §5 "What lands in the
  ledger" — **superseded in part by this ADR**), the ledger design's §6.1 (the pane is a
  block's durable anchor, `nocx-rtg0.28`), AD-8 (one owner per behaviour), `nocx-4em1z`,
  `nocx-69x9e`.

## Context

The assistant design gave each ask three ledger identities: the frame it referenced, an
entry for the **question**, and a second entry for the **answer**, joined to the question
by a `caused-by` edge. The reason §5 gives is about the answer's BODY: derived text is an
artifact with provenance and never "a string held in a map that dies with the process".

Two things were then observed in the shipped product.

**Dialogues did not survive a restart.** The restore read is `WHERE pane_id = ?` — the
pane is a block's durable anchor because it outlives the backend, while a session does not
(D5). `SubmitAgentAsk` wrote both of its entries with `session_id` and **no** `pane_id`, so
a restored tab found nothing at all: not a mis-rendered dialogue, an absent one. Nobody
had decided that; `entries` has two writers, and the pane-anchor decision reached
`Submit` and never `SubmitAgentAsk`, whose own input struct had no pane field to fill.

**And two rows for one block cost the reader a fold.** On screen a turn has always been
ONE block — the question in the header, the answer in the body — which is the same shape a
command block has. Restoring two rows meant deciding, per row, whether it was a question
needing its answer attached or an answer needing its question, from a link the wire did
not carry. The owner, shown this: «вопрос — это команда. А ответ — вывод. Зачем что-то
изобретать?»

## Decision

**A turn is one entry.** `kind = agent`, the question is `intent`, and the answer is an
artifact on the turn's own run execution. There is no answer entry and no `caused-by` edge
between them.

> **Amended 2026-08-23 by [ADR-0040](0040-a-block-is-a-node-in-an-ordered-tree.md).** The
> first sentence stands: a turn is one entry, and that entry is its IDENTITY — the id the
> deltas route by, the grant hangs off, and copy and the conversation address. The second
> no longer does. **A turn's answer is not an artifact of its own; the turn's body is its
> CHILDREN**, one `kind = text` block per run of prose, ordered among the calls and
> commands the turn made. See the second amendment at the foot of this file.

**The turn is anchored to its pane, and the transport is what anchors it.** The renderer
does not send a `paneId`: the backend already resolved which pane a session is the pipe
of, and `ledger.open` states in its own comment why a second copy on the wire is wrong —
"a paneId on the envelope would put the same input under a second owner, and the
renderer's copy would be the one nobody checked".

**One id on the wire.** `agent.ask` answers with `entryId` where it used to answer with
`questionId` and `answerEntryId`. Deltas, reasoning, tool-call lines and the copy path all
address the turn.

## Consequences

**§5 of the assistant design is superseded on this point and only this point.** Its stated
reason survives intact: the answer is still an artifact with provenance, not a string in a
column (ADR-0019 §6). What is dropped is the answer's separate identity — which nothing
used as an identity. It was an ADDRESS for the stream, and the turn's own id is that
address.

**Restore needs no new fact to tell a turn from a command.** A command's drawn body is
`application/vt`, with its `text/plain` copy marked `derived_from` it; a turn's body is a
`text/plain` original and no terminal body is ever written for one. So the block's grammar
— a grid must not re-wrap, prose must — is read from a stored fact. A stored `role` column,
a backend-derived `role`, and splitting author from kind were each considered for this and
are each unnecessary. The last remains true as a defect and is filed separately
(`nocx-69x9e`): `entries.kind` is documented as the author and also carries what the row
is, so a question the person typed is stored saying the agent authored it.

**A turn is a block, so it appears where blocks appear.** (**Amended 2026-08-23 by
`nocx-9sqii` — a turn is drawn as SEVERAL blocks, one per fragment of its answer, with
the blocks it caused between them. See the amendment at the end of this file; the rest
of this paragraph stands.**) `blocks.list` therefore offers
the model only entries that have ENDED — otherwise the open entry in the pane is the
question being answered right now, and the model is handed its own unanswered question as
context. The same rule covers a command still running.

**The reasoning is not persisted, deliberately** (the owner's call, same session): it is
several times longer than the answer and closed by default, so a restored turn has no
reasoning note at all rather than an empty one.

**A tool call stays its own entry and stays out of the restore** — "an action has no block
and no command line". It is drawn as a line inside the turn's flow. It is currently
anchored to nothing at all, which means a restored turn comes back without the calls it
made; `caused-by`, freed by this decision, is the relation that will join an action to its
turn.

## Amendment, 2026-08-23 — a turn is drawn as FRAGMENTS around the blocks it caused (`nocx-9sqii`)

**The sentence amended is "A turn is a block, so it appears where blocks appear."**
It is now: **a turn is one ENTRY, drawn as one or more blocks — one per fragment of
its answer — and the blocks it caused stand between them.**

**Why.** The owner asked «Как мне проверить сколько места на диске?» and read, in this
order: the question, a bare `▸ run` line carrying neither arguments nor result, the
finished answer «41G свободно, занято 79%», and THEN — below the whole turn — the
`df -h` block with the twelve lines the answer was distilled from.

```
causal:  question -> tool call -> evidence -> answer
drawn:   question -> call marker -> answer  -> evidence
```

One command occupied two positions and the useful half was in the one nobody reads
first. The cause is exactly the amended sentence: if a turn is ONE block, and a
command the turn runs is ANOTHER block appended at the tail of the scrollback, then
the answer necessarily finishes above the evidence it was written from. No amount of
work inside either block can fix an ordering the block model forbids.

**What is dropped.** Only the identity between a turn and a single block. A turn that
ran a command now draws several: the question and the prose before the first command,
that command's own block, the prose written from it, and so on. The fragments are ONE
turn and say so from a stored fact — every one carries the turn's `data-entry-id`, and
a continuation carries its index and the kit's `continued` badge. A reader must never
have to tell a continuation from a second answer by a colour.

**What is untouched, and why it did not need to change.**

- **A turn is still one ENTRY** — the whole of this ADR's decision. Nothing about the
  ledger changes: one row, `kind = agent`, the question as `intent`, the answer as one
  artifact on the turn's run. The fragments are a PROJECTION of that entry
  (ADR-0019: one authoritative ledger, disposable projections), and the arrangement is
  a projection OF the relation, never a fact the renderer stores.
- **`blocks.list` still offers only entries that have ENDED.** The reason given above
  is about the OPEN entry in the pane, not about how many elements it draws.
- **`nocx-shxv0`'s ownership rule survives verbatim: the BLOCK owns the command, the
  answer's LINE owns WHEN.** What was wrong was never that rule — it was that the two
  owners were drawn in different neighbourhoods. So for a tool that opens a block the
  line is now gone and the block stands in its place, saying WHEN by standing there;
  for a tool that opens none, the line is still the only thing that says the call
  occurred, which is `nocx-nm9md` narrowed to `readScreen`, `blocks.list` and
  `blocks.read`.
- **The reasoning is still not persisted**, and a restored turn still has no reasoning
  note at all.

**What the fragments needed that did not exist.** `nocx-h1l4o` gave every caused entry
a causal POSITION, which orders the causes and does not say where in the prose any of
them sat — so a restored turn put its calls at the head of its flow while the live one
interleaved them. The amendment closes that deliberately-deferred gap by making the
anchor a stored fact too: `caused-by`'s payload carries `at`, how much of the answer
had been written when the cause was recorded, in UTF-16 code units. The unit is the
reader's: the renderer cuts a JavaScript string, and a byte offset would split «занято»
mid-character on the first Russian answer this feature was reported against.

**Two arrangements were considered and rejected, and they are not open for
re-litigation without amending this ADR again.**

- **Nesting the command's block inside the turn's body.** It puts a fixed VT grid,
  which must not re-wrap (`nocx-juau`), inside reflowing prose — and the nested block
  then has to be argued back into being a first-class block for selection, copy,
  re-run and block navigation, which is the whole of what it already was.
- **Hoisting the command above the question.** In a terminal, vertical position is a
  claim about time, and that claim would be that the command preceded the intent that
  caused it.

## Amendment, 2026-08-23 (second) — the answer is not one artifact; the turn's body is its children ([ADR-0040](0040-a-block-is-a-node-in-an-ordered-tree.md))

The first amendment above bought the interleaving with an ANCHOR: `caused-by`'s payload
carries `at`, the offset into the answer where each cause sat, and the renderer cuts the
stored answer at those offsets. ADR-0040 removes both the cut and the offset, and the
reason is this ADR's own: it retired the separate answer entry because that entry "was only
ever an ADDRESS for the deltas". `at` is the same shape one layer down — a coordinate that
exists only because the unit that is DRAWN (a run of prose) and the unit that is STORED
(the whole answer) were different things.

**What is dropped.** The answer artifact on the turn's run; the `caused-by` edge and its
`{pos, at}` payload; the fragment arrangement this file's first amendment recorded, along
with the `continued` badge and the per-fragment identity that came with it.

**What survives.** A turn is one entry and one block — more firmly than before, since it is
no longer drawn as several. One id on the wire. The pane anchor, and the transport as what
anchors it. The answer as an artifact with provenance rather than a string in a map — there
are simply several of them now, one per prose block, each owned by the block it is the body
of.

**Both arrangements this file rejected stay rejected, and one of them for a re-read reason.**
Hoisting the command above the question still claims the command preceded the intent that
caused it. Nesting is subtler: what was rejected was nesting a VT grid _inside reflowing
prose_, and that is still refused. A child block in an ordered tree is not that — it is a
sibling of the prose rather than a hole inside it, held to exactly the rules any block is
held to, so `nocx-juau` binds it unchanged and it reaches its far end by the horizontal
scroll `.cmd-output` already has.
