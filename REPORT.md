# Report — nocx-zg3k3.4.5

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
