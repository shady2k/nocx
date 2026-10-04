# Report — nocx-xn63t.6.18

## Reproduction and hypothesis

The requested WebKit container run passed twice, then reproduced the reported failure on the third run at Clause 2: the spec expected `scrollTop` 0 and received 1280. This is the spec seam (hypothesis B), not a product scroll-anchor defect. Clause 2 submitted the command while the reader was scrolled up, then captured `scrollTop`; command submission itself could move the scroller. The captured numeric offset therefore did not identify the row the reader was viewing.

## Change

The spec now arms its delayed-output producer while following the live end, waits for an explicit `ANCHOR-ARMED` row, then returns to history before the delayed output arrives. It records a visible history row by its text and checks that the same row stays at the same viewport position after the output. This tests the reader's actual visual anchor rather than a browser-dependent `scrollTop` value. No product code changed.

## Validation

- Before the change: 3 WebKit container runs; 2 passed, 1 failed with `Expected: 0`, `Received: 1280`.
- After the change: 3 consecutive WebKit container runs passed (1 test each).
- Chromium container run passed (1 test).
- `npx prettier --check e2e/terminal-scrollback-surface.spec.ts` initially reported formatting needed; the file was formatted with Prettier.
- `git diff --check` passed.

## Commit and push

Commit: `9989e5e4` (`fix(e2e): anchor scrollback checks to visible history rows`). Push pending.

## Not done

No product fix was needed. `make ci-full` was not run, per the brief.
