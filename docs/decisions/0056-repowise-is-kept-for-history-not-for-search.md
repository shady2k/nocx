# ADR-0056 — repowise is kept for history, not for search

- **Status:** Accepted
- **Date:** 2026-09-07
- **Related:** AGENTS.md "Code search" (the rule this amends), AD-8 (one owner per
  behaviour — the reason a second answer to "where is this code" is a cost, not a
  convenience).
- **Beads:** `nocx-14sbw` (the measurement, both passes), `nocx-bw9j8` (this
  reconciliation), `nocx-n5gr2` (the stale comment repowise repeated as fact),
  `nocx-p64tn` (the read hooks, still unmeasured).
- **Consulted:** the owner, 2026-09-04, who adopted repowise against the search
  verdict below and asked for it to be tried in anger; and again 2026-09-07, who
  asked why agents were not using it.

## Context

AGENTS.md carried a standing prohibition, bought by graphify: a code index was
removed on 2026-08-01 after answering none of five real questions while committing
91 MB, and nothing of that class was to come back "without measuring against the
baseline".

repowise is that class of tool. The measurement was run rather than argued
(`nocx-14sbw`), twice, on five questions taken from real sessions:

- **Keyless**, with no embedder: 0 of 5 correct, confidently wrong twice.
- **Keyed**, with a live embedder and provider: 2 correct, 1 partial, 1 honest
  miss, and 1 confidently wrong — it named a symbol as having no production caller
  by repeating the stale comment sitting on that accessor, while six call sites
  existed. `grep` answered all five in under 0.1 s and cannot repeat a comment at
  you.

One layer came out differently. `get_risk` reads git rather than the code: hotspot
scores, dependents, bus factor, and co-change partners with support counts —
including `internal/transport/ws.go` paired with `frontend/src/terminal-content.ts`
at support 35, a cross-language coupling with no import and no structural link.
Verified independently against git log. No search can produce that.

The owner then adopted the tool on the real repository, which left AGENTS.md
reading as a flat prohibition against something the repository now contains. Left
alone, the next agent removes it BY INSTRUCTION and is right to.

## Decision

**repowise stays, and the rule says what it is for.** `grep`, `glob` and reading
the file are the answer for _does this exist, and who calls it_. The MCP tools are
for the layer underneath: history, risk, co-change, and the rationale behind a
shape. That is an ordering, and it is now written down with the numbers that bought
it rather than as a preference.

**Its prose is never a fact about the tree.** It inherits whatever our comments
claim, stale ones included. Anything it says about code is confirmed by reading the
code.

**The graphify paragraph survives unchanged.** The baseline and the reason it was
removed remain what any future index is measured against. This ADR is not a licence
for the next one; it is the record of one tool that was measured and kept for the
one layer it won.

## Why this rather than the obvious alternative

The obvious alternative was to leave the text saying the two are co-equal and let
each agent pick. That is what it said until now, and it is what produced the
question this ADR answers: agents were not using repowise, and they were right not
to, because for the question they were asking it was slower and less precise. A
document that declines to rank two tools does not create a free choice — it makes
every reader re-derive the ranking, badly, from whatever they tried last.

The second alternative was to drop the tool, as graphify was dropped. The
co-change layer is what prevents that: it is real signal, verified against git, and
nothing else here produces it.

## What the next person inherits

An ordering they can check: the five questions and both passes are in
`nocx-14sbw`, with timings.

A configuration hazard, now in AGENTS.md: repowise's `_DIMS` table declares
`google/gemini-embedding-001` at 768 dimensions while the model returns 3072, so
without `REPOWISE_EMBEDDING_DIMS=3072` every vector fails its width check and none
is written. The index then answers from BM25 alone — the configuration that scored
0 of 5. It was in exactly that state on the real repository from adoption until
2026-09-07, which is the whole reason the question "why do agents not use it" had
a good answer.

The override is needed twice: for `repowise reindex`, and in the environment of the
MCP server, which embeds the query. Both are wired — the server from the tracked
`.mcp.json`.

And one reading lesson: `repowise doctor`'s `SQL ↔ Vector Store: in sync` passes
while both sides are empty. `Coordinator drift` is the row that tells the truth. It
was written off here once as the broken check while it was correctly reporting 3699
pages against 0 vectors.
