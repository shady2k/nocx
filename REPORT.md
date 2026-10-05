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

### Wired

The selection gesture now resolves card endpoints by `(block id, artifact version, logical line)` from the parsed `StoredBlockRows` retained by `paintStoredRows`. It hit-tests the endpoint against the live painter's committed cell width and uses the shared mapping's grapheme-cluster column resolver, so a wide grapheme remains atomic. Card content is reconstructed from the stored rows through `captureLiveSelection`, the same copy primitive used by the pinned live snapshot; rendered text and node order are not copy inputs. Cross-boundary ranges are joined in display order (card rows before live rows), independent of drag direction. The live portion remains highlighted while a cross-boundary drag is active.

### Tests

Added `live selection gesture > copies a drag from live output into a card in display order using stored rows` in `frontend/src/painter/selection-gesture.test.ts`. It went red first: release over the card made zero clipboard calls. After the implementation it passes. The browser-level test covers forward and reverse drag directions, a frame arriving during the gesture (the copied live text remains from the pinned revision), highlight presence, subsequent frame paint, replacement/reordering of painted rows, wide-grapheme hit testing, soft-wrap joining and hard-newline preservation. Existing `.4.4` test still covers live-only pinned selection.

### Checks

- `cd frontend && npx vitest run src/painter/selection-gesture.test.ts src/painter/mapping.test.ts src/scrollback/block-rows.test.ts` — passed, 36 tests.
- `cd frontend && npx tsc --noEmit -p tsconfig.json` — passed.
- `cd frontend && npm run format:check` — passed.
- `npm run contracts:check` — not run; no wire contract changed.
- Containerized jobs, `make ci-full` and e2e — not run, as instructed.

### Not done

Cross-card selection between two different card artifacts is not supported; this leaf implements the specified cross-boundary card/live gesture and same-card selection. Selection across multiple historical cards would need a display-order model for the intervening artifacts, which is not represented by the endpoint identity currently carried on each row.

## Session — accessible rows and block structure (nocx-zg3k3.4.2)

### Built

The live cell painter now exposes a labeled grid with readable row text, row positions, roving keyboard focus (Arrow Up/Down, Home, End), current/focused state, and selection state. It paints a visible focus outline and marks the cursor and selection overlays as decorative. It does not use a live region: routine frame and dirty-cell updates therefore do not create repeated announcements.

Scrollback blocks now expose labeled, keyboard-focusable groups in DOM order. Their focus outline is visible. Keyboard navigation changes focus without activating a block.

### Tests and TDD evidence

- `src/painter/painter.test.ts` — `accessible live rows > moves keyboard focus through readable rows without activating them`; `accessible live rows > exposes updates without a live announcement channel and ignores identical frames`.
- `src/scrollback/blocks.test.ts` — `accessible block structure > exposes each block as a named, keyboard-focusable group`.
- Red first: with the product implementation removed, the acceptance tests failed (4 failures, 227 passed). Failures included absent grid role, missing row state, and missing block group role. The targeted tests then passed with the implementation.

### Checks

- `cd frontend && npx vitest run src/painter/painter.test.ts src/scrollback/blocks.test.ts` — passed, 2 files / 231 tests.
- `cd frontend && npx tsc --noEmit -p tsconfig.json` — passed.
- `cd frontend && npm run format:check -- -- <touched files>` — passed.
- No wire changes; `contracts:check` was not applicable. The prohibited full/container/e2e suites were not run.

### Not done

The manual screen-reader pass is not complete. This worktree has no interactive screen-reader session for a named platform. Stage acceptance still needs a recorded pass with the platform and screen reader named, confirming row and block reading and that routine dirty-cell updates are not noisy.

### Commit

Pending.

## Session — joined live-to-card browser acceptance (nocx-zg3k3.4.3)

### Added

`e2e/live-to-card-joined.spec.ts` drives one Chromium user path across a historical command card and the live cell grid. It keeps a real PTY `cat` running and sends it input while the pointer drag is held, which drives a newer screen revision without a timer or timing-dependent setup. The fixture generates a long output line in the shell so the PTY input path cannot truncate it. The clipboard assertion checks the exact soft-wrapped line joined without an inserted newline, the hard newline between output lines, the live output, and that the later frame's output was not copied from the pinned snapshot. It then checks the live grid and card accessible roles, keyboard row navigation, absence of an `aria-live` channel, and visibility of the latest output after release.

`e2e/manual-screen-reader.md` gives the repeatable macOS VoiceOver acceptance procedure and the fields to record. The named-reader pass remains for the owner at stage acceptance; it was not performed in this worker session.

### Red-first evidence

The new browser spec was added before changing any product code. Red-first runs found an ambiguous live-row locator, an output marker also present in the shell command echo, an overlong typed fixture that lost characters, and time-based frame injection that did not reliably overlap the drag. The test now generates its long output inside the shell and sends input to an interactive PTY `cat` while the pointer is held, which triggers the frame on demand. The final targeted browser run passed. These were acceptance-harness defects; no product implementation changes were needed.

### Checks

- `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/live-to-card-joined.spec.ts` — passed, 1 Chromium test.
- `cd frontend && npx vitest run src/painter/selection-gesture.test.ts src/painter/painter.test.ts src/scrollback/blocks.test.ts` — passed, 3 files / 233 tests.
- `cd frontend && npx tsc --noEmit -p tsconfig.json` — passed.
- `npm run typecheck` — passed for the e2e TypeScript project after removing a duplicate global declaration.
- `npm run format:check` — passed after formatting the new files and report.
- Pre-commit checks — passed, including root ESLint and the e2e TypeScript check. An initial commit attempt caught an unused fixture variable; it was removed before the successful commit.
- `npm run contracts:check` — not run; no wire changes.
- No full e2e suite or `make ci-full` was run.

### Commit

- `c5abe521` — `feat(frontend): prove joined live-to-card selection (nocx-zg3k3.4.3)`.
