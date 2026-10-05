# Report — nocx-xn63t.6.15

## Commits

- `5375c486` — implementation and acceptance tests.

## Changes

The renderer now sends the active tab ID as `afterTabId` and does not compute a tab position. The `tabs.create` handler routes anchored creates through `CreateTabAfter`, which seats and renumbers the workspace in the content store. The params contract and renderer request type carry the anchor.

## Tests and red-first evidence

- Added `LayoutStore > asks the store to seat a new tab after the active tab without computing a position` in `frontend/src/layout/layout-store.test.ts`.
- Red-first run after adding the assertion: 1 failed, 22 passed. The failure showed `afterTabId` was absent from the request. It passed after implementation.
- Added `TestTabsCreateAfterActiveTabSeatsAndReadsBackOrder` in `internal/transport/ws_layout_read_test.go`. It checks ordering after a fresh connection read and verifies the other workspace remains unchanged. The targeted transport test passed after fetching pinned Ghostty archives with `make vt-archives`.
- Updated existing renderer test fixtures to stop supplying positions to the `createTab` API.

## Checks

- `cd frontend && npx vitest run src/layout`: PASS (4 files, 42 tests).
- `cd frontend && npx tsc --noEmit -p tsconfig.json`: PASS.
- `cd frontend && npx tsc --noEmit -p tsconfig.test.json`: PASS.
- `cd frontend && npx vitest run src/panes-layout.test.ts src/panes.test.ts`: PASS.
- `cd frontend && npx prettier --check src/layout/layout-store.ts src/layout/layout-store.test.ts src/layout/layout-client.ts src/panes.ts src/test-support/panes-fixtures.ts src/panes-layout.test.ts src/panes.test.ts`: PASS.
- `cd frontend && npm run contracts:check`: PASS.
- `go test ./internal/content ./internal/capability`: PASS.
- `go test -tags gtk3 ./internal/transport`: PASS (the focused new test and package suite). `make vt-archives` supplied the pinned Ghostty headers required to build transport.
- `git diff --check`: PASS.

`npm ci` was needed to install the root and frontend dependencies in the worktree.
