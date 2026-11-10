# Paged output cache follows accepted retention

Correction to PR #789 at `33f48cd885a9ae95b2bc7cad9cde1f77f00934de`, tracked as
nocx-4y82c.1.24. This paging implementation is not yet on main.

`mergeStoredBlockRows(previous, next)` keeps loaded rows only inside the accepted
`next.retainedRanges`. The store owns retention; a previously loaded page does
not extend it. An explicit empty range set evicts every cached row. Missing
geometry preserves the existing fallback, while an unloaded retained range
continues to reserve space and is not reported as lost output.

For example, loaded rows `0,1,2,3` followed by rows `8,9` with retained ranges
`[0,2),[8,10)` must produce `0,1,8,9`, a six-row gap, and no selectable rows `2,3`.
The tests exercise this transition and repeated cap crossings through the actual
painter and selection snapshot, as well as unloaded spacers and artifact changes.

Run `npx vitest run src/scrollback/block-rows.test.ts` from `frontend/`.
The correction passed all 66 module tests and a Chromium surface importing the
real parser, merger and painter: 200 updates retained at most eight cached,
painted and selectable rows, with exact surviving indices and one gap marker.

The original merge retained 50,000 rows after 500 deliveries of 100 rows despite
a 400-row retention window. The corrected cache and painter retained 400.
This bounds this cache by the accepted retained rows, not the whole browser's
RSS or the application's live-emulator storage.

The caller still owns freshness: `next` must be the accepted current read.
Artifact identity is not a mutation revision. This correction does not solve
competing page/refresh response ordering; nocx-3an1s.1.16 owns the bounded,
revision-consistent-read design. Do not infer that PR #789 is ready to merge from
this component regression alone.
