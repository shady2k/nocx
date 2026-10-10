# Report — nocx-zg3k3.3.2 helper effect carrier

## Wired

The helper now carries each runtime effect as its own `TypeSessionEffect` frame, separate from `TypeScreenFrame`. The frame preserves the session and subscriber ids, runtime generation, effect id, kind, and body. The helper session drain sends each effect identity once per subscriber drain. The client delivers it only to the matching attachment. Unknown kinds and malformed frames are refused. Full screen snapshots remain effect-free.

This is the helper-carrier slice only. Effects are not yet forwarded by the app to the WebSocket client, no `contracts/` schema or generated frontend type was added, and the renderer handlers, permission flow, and replay/reconnect deduplication are not wired. Those are required follow-up work; this commit does not complete the bead's end-to-end acceptance. No schema was added because this commit only changes the helper's binary frame protocol.

## Tests and red-first evidence

Added `TestEffectFrameRoundTripsIdentityAndBody`, `TestEffectFrameRefusesUnknownKind`, `TestEffectFrameRejectsShortHeader`, `TestTheEffectFrameTypeIsInTheClosedSet`, `TestASubscriberReceivesAnIdentityBearingEffect`, and `TestAnEffectFrameReachesItsNamedAttachment`.

The first test attempt was blocked because the pinned libghostty-vt archive was missing. After `make vt-archives`, I temporarily removed the new codec implementation and reran the new codec tests; they failed on the missing `EffectFrame`/codec symbols. Restoring the implementation made them pass.

## Checks

- `make vt-archives` — passed.
- `gofmt` on all touched Go files — passed.
- `go test ./internal/helper/session ./internal/helper/proto ./internal/helper/host ./internal/helper/client -count=1` — passed.
- `go vet -tags gtk3 ./internal/sessionruntime/` — passed.
- `go test -tags gtk3 -count=1 ./internal/sessionruntime/` — passed.
- `go vet ./internal/helper/session ./internal/helper/proto ./internal/helper/host ./internal/helper/client` — passed.
- `git diff --check` — passed.

## Commit

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
