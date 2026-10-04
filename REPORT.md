# Report — nocx-zg3k3.4.5

## Commits

- `898626cd` — `feat(frontend): carry card selection endpoint identity (nocx-zg3k3.4.5)`

## Wired

The restored card-row path now carries the immutable ledger artifact ID as `artifactVersion` through `StoredBlockRows`. Painted card rows expose `blockId`, `artifactVersion`, and the stored absolute `logicalLine` as data attributes. Restore reads pass the artifact ID into the parser. The artifact ID is used as the version identity because ledger artifact bodies are addressed by immutable ID; no wire schema change was needed.

## Tests

Added `stored block rows > carries the immutable artifact version and logical line onto painted card rows` in `frontend/src/scrollback/block-rows.test.ts`. It went red first: `row.dataset.blockId` was undefined (expected `block-7`). It passes after the implementation.

## Checks

- `cd frontend && npm ci` — passed (385 packages installed; npm reported 8 audit advisories).
- `cd frontend && npx vitest run src/scrollback/block-rows.test.ts src/restore-client.test.ts` — passed, 65 tests.
- `cd frontend && npx tsc --noEmit -p tsconfig.json` — passed.
- `cd frontend && npm run format:check` — passed.
- `npm run contracts:check` was not run; no wire contract changed.

## Not done

The core drag-release-into-card behavior is not implemented. The live selection gesture still refuses outside-grid releases, and there is no combined live/card selection using the pinned `.4.1` copy primitive. Therefore the end-to-end clipboard behavior, live highlight through card release, soft-wrap/hard-newline range copying, DOM-reordering invariance, and grapheme-boundary card hit testing remain unverified. No e2e suite was run, as instructed.

## Session 2 — live-to-card selection

### Commit

- `3cbfcfa1` — `feat(frontend): copy selections from live output into cards (nocx-zg3k3.4.5)`

### Wired

The selection gesture now resolves card endpoints by `(block id, artifact version, logical line)` from the parsed `StoredBlockRows` retained by `paintStoredRows`. It hit-tests the endpoint against the live painter's committed cell width and uses the shared mapping's grapheme-cluster column resolver, so a wide grapheme remains atomic. Card content is reconstructed from the stored rows through `captureLiveSelection`, the same copy primitive used by the pinned live snapshot; rendered text and node order are not copy inputs. Cross-boundary ranges are joined in display order (card rows before live rows), independent of drag direction. The live portion remains highlighted while a cross-boundary drag is active.

### Tests

Added `live selection gesture > copies a drag from live output into a card in display order using stored rows` in `frontend/src/painter/selection-gesture.test.ts`. It went red first: release over the card made zero clipboard calls. After the implementation it passes. The browser-level test covers forward and reverse drag directions, a frame arriving during the gesture (the copied live text remains from the pinned revision), highlight presence, subsequent frame paint, replacement/reordering of painted rows, wide-grapheme hit testing, soft-wrap joining and hard-newline preservation. Existing `.4.4` test still covers live-only pinned selection.

### Checks

- `cd frontend && npx vitest run src/painter/selection-gesture.test.ts src/painter/mapping.test.ts src/scrollback/block-rows.test.ts` — passed, 36 tests.
- `cd frontend && npx tsc --noEmit -p tsconfig.json` — passed.
- `cd frontend && npm run format:check` — passed.
- `npm run contracts:check` — passed in the pre-commit static gate; no wire contract changed.
- Containerized jobs, `make ci-full` and e2e — not run, as instructed.

### Not done

Cross-card selection between two different card artifacts is not supported; this leaf implements the specified cross-boundary card/live gesture and same-card selection. Selection across multiple historical cards would need a display-order model for the intervening artifacts, which is not represented by the endpoint identity currently carried on each row.
