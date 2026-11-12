# Paged output cache follows accepted retention

The measurements behind `nocx-4y82c.1.24`. What a capped block keeps is the question here;
the decisions it led to are in
[ADR-40d9xt5n](../decisions/40d9xt5n-a-blocks-cached-rows-follow-the-retention-they-were-accepted-under.md).

## Sustained cap crossing

A Chromium surface importing the production parser, merger, painter and selection code
delivered 500 batches of 100 rows, with a 400-row retained head/tail window. Halfway
through, `outputStartRow` advanced from 0 to 50. The same scenario against a real backend is
`e2e/paged-retention.spec.ts`.

| Observation                        | Before the correction |   After |
| ---------------------------------- | --------------------: | ------: |
| Maximum cached rows                |                49,950 |     400 |
| Final cache / DOM / selection rows |                49,950 |     350 |
| Cap gap notices                    |                     0 |       1 |
| Total merge-only time              |              586.5 ms | 15.1 ms |

The final retained indices were exactly `50..99` and `49700..49999`, the tail text was
`row 49999`, and the gap stated 49,600 missing rows. Explicit empty ranges removed the
output and selection; unloaded retained ranges still reserved their exact height without a
false loss notice. The uncorrected code reproduced the accumulation and failed full
eviction.

These are single component measurements, not native application throughput or RSS bounds.
Parser and paint time is excluded from the timing, and the DOM was painted at the same
checkpoints on both sides rather than on every batch.
