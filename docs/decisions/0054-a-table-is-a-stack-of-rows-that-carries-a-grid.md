# ADR-0054 — A table is a stack of rows that carries a grid

- **Status:** Accepted.
- **Date:** 2026-08-31
- **Related:** AD-6 (single-owner state ownership: the renderer owns the answer's
  row model), AD-8 (one owner per behaviour), `frontend/src/ui/README.md` (the kit
  grows by variants, never by near-duplicates),
  [ADR-0039](0039-an-assistant-turn-is-one-entry.md) (an assistant turn is one
  entry). Beads `nocx-gxr9w.10` (the report), `nocx-swoje` (`AnswerMarkdown`
  itself), `nocx-hp8p2` (the epic that says the answer should read like a
  document).

## Context

The model answered a comparison with a markdown table and the person saw the
pipes and the dashes.

That is not an oversight, and `frontend/src/ui/answer-markdown.ts` says so in its
own header. The answer body is a **stack of `.term-line` rows, one per line**,
because that is what the scrollback IS, and three separate paths read that stack:
the selection path freezes a row range into a reference chip, the copy path reads
rows back as text, and the stream arrives in chunks that split mid-line. So the
module paints ONE COMPLETED LINE — the largest unit the stream can hand over
finished — and everything markdown expresses ACROSS lines is deliberately not
rendered: tables, setext headings, horizontal rules, nested quotes.

There is a second obstacle, and it is the one that makes "just gather the rows"
harder than it sounds. A markdown table is only recognisable at its **second**
line, the delimiter row `|---|---|`. By the time you know a table has started,
its first line has already been painted and appended.

So the question is not whether a grid can be drawn. It is what a grid costs the
row model, and the answer had to be decided before `answer-markdown.ts` was
edited at all.

## Decision

**A table renders, and it renders as the same stack of rows.**

Each line of the table stays exactly what it is today — one `.term-line`, painted
by the same per-line path, appended in order. The table's rows are additionally
given `display: contents`, and their cells become children of a grid declared on
the container. The row element remains in the DOM in its place; only its own
layout box goes away.

Column widths are then CSS's problem, not the painter's: the grid measures every
row it currently holds and re-measures as rows arrive. That is precisely the
across-lines computation a line-at-a-time painter cannot perform, handed to the
layer that can.

The reach-back problem dissolves with it. The first line does not need to be
retracted and re-rendered as part of a new element — it needs a class and a role,
which is an attribute change on a node the block already owns, in imperative DOM
(`scrollback/blocks.ts`) that has always been free to touch what it appended.

## Why this rather than the obvious alternative

The obvious alternative is the one the bead offered: lift the table's lines out
of the stack and build one grid element — a real `<table>`, or a `<div>` grid
holding cells directly.

It was rejected on ownership, not on effort. Today **nothing** has to know that a
table exists: `blockOutputText` collects `.term-line` and joins the text,
`codeText` in `scrollback/answer-body.ts` does the same for fenced code, and the
selection and chip paths walk the same rows. Every one of those was checked
before this was written, and none of them reads geometry from an individual row —
`getBoundingClientRect` appears in the scrollback on the block and on the
container, never on a `.term-line`. A separate table element would take those
rows out of the stack and make each of those paths learn about a second shape:
three owners of "what are the lines of this answer" where there is currently one,
which is the AD-8 failure this repository has paid for repeatedly. They would
agree in every case anyone tried and disagree on the one nobody did.

The second alternative — leave tables unrendered and instruct the model not to
emit them — was rejected because a prompt instruction is a request, not a
guarantee. On the turn the model does not comply, the person sees exactly what
they see today, and the product's behaviour would depend on a sentence rather
than on the renderer. `nocx-p5rjv` may still tell the model what shapes the
answer surface renders well; that is guidance, and it is not this decision.

## What the next person inherits

- The row model is unchanged, and it is still the single owner. If you are
  writing something that reads an answer's lines, keep reading `.term-line`.
- `display: contents` is load bearing. If a future change makes any consumer read
  an individual row's geometry — its height, its offset, its bounding rect — that
  consumer and this decision are in conflict, and the conflict is silent, because
  the value returned will be a zero-sized box rather than an error. A test must
  state the invariant: for a table row, the row is still found by
  `querySelectorAll('.term-line')` and still yields its text.
- Alignment markers in the delimiter row (`:---`, `:---:`, `---:`) are a property
  of the column, which is now a real thing that exists — they map to the grid's
  `justify-items`, and nothing about them needs the painter to reach across
  lines.
- A table whose rows disagree about cell count is the model's text, not an error.
  Render what arrived; do not pad, do not drop, and do not assert a shape the
  model did not write.
