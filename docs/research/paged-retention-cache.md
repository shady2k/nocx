# Paged output cache follows accepted retention

PR #796, tracked as nocx-4y82c.1.24. Updated on 2026-11-12 against main
`89d70c81e`, which includes PR #789 and the late output-start correction.

`mergeStoredBlockRows(previous, next)` keeps loaded rows only inside the accepted
`next.retainedRanges`. The store owns retention; a previously loaded page does
not extend it. An explicit empty range set evicts every cached row. Missing
geometry preserves the existing fallback, while an unloaded retained range
continues to reserve space and is not reported as lost output.
The same merge also applies `outputStartRow`: command echo below a late output
start must not survive merely because its physical rows remain in the artifact.

For example, loaded rows `0,1,2,3` followed by rows `8,9` with retained ranges
`[0,2),[8,10)` must produce `0,1,8,9`, a six-row gap, and no selectable rows `2,3`.
The tests exercise this transition and repeated cap crossings through the actual
painter and selection snapshot, as well as unloaded spacers and artifact changes.

Run `npx vitest run src/scrollback/block-rows.test.ts` from `frontend/`.
All 71 module tests passed after integration, including simultaneous output-start
advance and cap eviction with exact painted/selected text.

A Chromium surface importing the production parser, merger, painter and selection
code delivered 500 batches of 100 rows, with a 400-row retained head/tail window.
Halfway through, `outputStartRow` advanced from 0 to 50.

| Observation                        | Main `89d70c81e` | Corrected |
| ---------------------------------- | ---------------: | --------: |
| Maximum cached rows                |           49,950 |       400 |
| Final cache / DOM / selection rows |           49,950 |       350 |
| Cap gap notices                    |                0 |         1 |
| Total merge-only time              |         586.5 ms |   15.1 ms |

The final retained indices were exactly `50..99` and `49700..49999`, the tail
text was `row 49999`, and the gap stated 49,600 missing rows. Explicit empty
ranges removed the output and selection; unloaded retained ranges still reserved
their exact height without a false loss notice. All 14 browser observations
passed on the corrected code; the baseline reproduced accumulation and failed
full eviction.

These are single component measurements, not native application throughput or
RSS bounds. Parser/paint time is excluded from the timing. The DOM was painted
at the same checkpoints on both sides, not on every batch.

## Read ordering on the current main

Artifact identity is not a mutation revision. The original renderer let one page
and one growth refresh of the same entry run independently. A delayed page could
therefore restore old retained ranges after a newer capped refresh, undoing
eviction. Responses from the previous bind could also mutate its replacement.
Reconnect additionally compared the cache using the _new_ artifact id, missing
an artifact replacement when geometry stayed equal.

The correction extends the existing per-entry `_blockRowsInFlight` chain to
both pages and refreshes. Growth during a page queues the existing follow-up;
viewport demand during a refresh is recomputed when the chain settles. Only the
promise still owning the entry may paint or release it. Rebind and disposal
clear that ownership. Revalidation keeps the cached artifact's identity and
does not reuse a metadata hint whose cache snapshot changed while it was read.
Different entries still use the existing connection-wide priority queue and its
concurrency limit; the server protocol, slots and retry policy are unchanged.

Seven independently authored regressions cover cap ordering, pending viewport
demand, artifact replacement, and page/refresh completion after rebind or
disposal. Four failed on the integrated baseline for the intended reasons;
all seven passed after the ownership correction.

This prevents renderer page/refresh reordering without introducing a new wire
revision or claiming transactional metadata/body snapshots across the backend.
The broader revision-consistent-read design remains in nocx-3an1s.1.16.

## Consumer boundaries and sustained demand

Independent review found three additional boundaries, each reproduced before
its correction. A refresh completion now lets one pending viewport page run
before the next growth refresh; that page carries queued growth forward. The
held-response regression keeps growth pending while checking that head rows
appear and the latest tail still arrives.

`BlockManager.applyStoredRows` no longer treats the last loaded row as freshness
when both snapshots carry a comparable durable end. Eviction may reduce that
loaded cursor without reducing the span. A decreasing end is still rejected,
but equal end is not a revision clock: the controller owns read ordering.
Bound and pending blocks both accept full eviction at an unchanged end.

For live output with retained geometry but no loaded rows, the painter keeps
the body element while clearing obsolete children and row/segment caches. Its
consumer regression proves that old text disappears, an older span cannot
restore it, and a later retained page paints normally.

Final verification: all 924 tests in `terminal-content`, `scrollback/blocks`,
`scrollback/block-rows`, `restore-client` and `scrollback/row-read-queue` passed.
The final Chromium component run passed 15 correctness observations, including
the empty-but-retained live transition; the 50,000-row scenario still peaked at
400 cached rows and ended with exactly 350 cached, painted and selectable rows.
Two same-model independent reviews accepted their scoped corrections after
re-review; they did not run the checks themselves.
