# Report — nocx-zg3k3.4.4

## Wired

Added a cell-model-coordinate selection overlay to the live painter. Added pointer handling that pins the current screen snapshot on pointerdown and copies through `captureLiveSelection` on pointerup. Integrated the gesture into `TerminalContent` and disposes the listeners with the pane. The overlay is painted from row/column endpoints and uses the committed grid geometry. The path does not read painted row text for copy.

## Tests

- `src/painter/painter.test.ts` — `live selection paint > highlights the model endpoints and retains them when a new frame paints`.
- `src/painter/selection-gesture.test.ts` — `live selection gesture > copies cells from the pointerdown revision when frames advance during a drag`; validates pinned copy across frame replacement and a wide grapheme boundary.
- The new painter test first failed because `painter.setSelection` did not exist. The interaction test initially failed because its expected end boundary omitted the wide cell's declared span; the endpoint handling was corrected and rerun green.
- Existing `src/cell-model.test.ts` tests cover soft-wrap joining and hard-newline retention in the `.4.1` primitive.

## Checks

- `cd frontend && npm ci` — passed.
- `cd frontend && npx vitest run src/painter/painter.test.ts src/painter/selection-gesture.test.ts` — passed (18 tests).
- `cd frontend && npx tsc --noEmit -p tsconfig.json` — passed.
- `cd frontend && npx prettier --check` on all touched frontend files — passed.
- No wire changed. The pre-commit hook ran `npm run contracts:check` and it passed. Did not run `make ci-full`, containerized tests, or e2e.

## Not completed

The requested live-to-card drag and stable card endpoint identity `(block id, artifact version, logical line, grapheme offset)` are not implemented. The existing cell-model primitive only copies a live snapshot; the card-row painting path does not expose the required artifact version and logical endpoint identity to a shared selection model. The current gesture deliberately refuses an endpoint outside the live grid instead of silently copying a truncated live-only range. The added interaction test is a Vitest/jsdom test, not a browser-run live-to-card test. These are remaining acceptance gaps, not claimed as complete.

## Commit

Commit: `54b513b2`.
