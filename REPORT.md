# Report — nocx-xn63t.6.19

## Diagnosis

This is a product race, not a fixed-schedule assertion. The acceptance waits for observable states: `R150` appears in the pane output ring, the command writes its `done` file, then the replacement coordinator reads both the stored row count and artifact seal. Its 20 ms sleeps only poll those state changes; no fixed delay defines the restart point.

The failure logs show the initial artifact starts at a variable absolute row index, after the command has already emitted output. In run 1, the first stored batch is `from=138 rows=32`; in run 2 it is `from=121 rows=32`. Run 2's lifecycle log says `lifecycle envelope ingested ... kind=start after_ms=1097` at 23:48:54.779, after the first rows were already processed. The data plane and authenticated lifecycle frames are separate streams, so the row frames can reach `BlockRowsArrived` before the block is opened by `openAttemptFor`.

At `internal/transport/ws_block_rows.go`, `BlockRowsArrived` handles rows with no current block by returning without confirmation. The buffered path only holds rows when `waiting`/`opening` was reserved first. Since the start event has not reserved/opened the block yet, rows that belong to the command can be refused before the lifecycle event arrives. The later rows are accepted, and the end marker still seals at its full logical cursor; sealing does not prove the row body was complete.

Representative exact CI log evidence:

- Run 1: `block rows direct append: stored ... from=138 rows=32 upTo=170` and then `... from=170 rows=7 upTo=177`.
- Run 2: `block rows append accepted past the continuity check ... from=121 lost=0 rows=32`, followed by `... from=153 lost=0 rows=1` and `... from=154 lost=0 rows=23`.
- Run 2: `lifecycle envelope ingested ... kind=start after_ms=1097`.
- Both runs later log replay batches through `from=273 rows=4 upTo=277`, then `interval end sealed the block ... cursor=300 endRow=277`; nevertheless the read sees only 162 rows (run 1) or 179 (run 2).

The VT runtime already owns the exact boundary: the first OSC 133 C mark records the absolute row-stream index in `OutputStartStreamRow`. The fix carries that position on a new closed-registry rows frame (`TypeOutputStartRow`, type 17), never on the lifecycle envelope. The transport only opens a block from authenticated lifecycle Start. Once Start and the mark join, it asks the same helper attachment to replace its reader, reusing the existing retained-window resend. The helper sends the output mark before the retained rows, including when the suffix is empty. The transport gates rows until that replay mark, drops the pre-replay unconfirmed deliveries, clips rows below the boundary, and confirms rows only after storage. This repairs the prefix without buffering unauthenticated rows or attributing prompt output to the command.

## Round-2 commits and squash status

All checked individual commits are not ancestors of `origin/main` at `7e6004254`: `8de90ed46`, `bee57fded`, `37345ecbc`, `885b691f8`, `434b7406c`, and `4cab5cbcf`.

However, `origin/main` contains commit `b5080c2e3` (PR #263), whose squash includes the retained-window resend, replacement-reader replay, and replay-before-queued-rows changes. `git blame` on `internal/helper/session/session.go` and `rows.go` attributes those code lines to `b5080c2e3`. Thus the individual commit ancestry is absent, but those changes are present in the main tree. The failure is in the remaining cross-channel start-boundary/admission window, not those already-squashed resend fixes.

## Validation and status

- Restart acceptance after the correction: `go test -tags gtk3 -count=5 -run "^TestABlockEndsWithTheWholeOutputAfterACoordinatorRestart$" ./internal/app` — passed 5/5.
- Full app package: `go test -tags gtk3 -count=1 ./internal/app` — passed on the corrected tree (171.7 s).
- The two app tests exposed by the first full run, `TestCapture_SaveNowAndSaveLaterOverTheRealSocket` and `TestAnAttachToAnIdleSessionWithNoSizeReceivesTheScreenItShows`, passed after preserving the existing writer epoch on a same-subscriber attach.
- Full helper-session and transport packages: `go test -tags gtk3 -count=1 ./internal/helper/session ./internal/transport` — passed (147.5 s and 264.3 s).
- Retained-window resend and position-mark order/empty-suffix tests passed in `internal/helper/session`.
- `go test -tags gtk3 -count=1 -run "^TestOutputStart" ./internal/transport` — passed, including the current-block replay mark queued-attempt regression test.
- Vet: `go vet -tags gtk3 ./internal/sessionruntime ./internal/helper/proto ./internal/helper/client ./internal/helper/session ./internal/app ./internal/transport` — passed.
- `git diff --check` — passed.
- Containerized block regressions: `e2e/run-in-container.sh e2e/notification-block-finished.spec.ts e2e/block-actions-keyboard.spec.ts e2e/notification-centre.spec.ts e2e/block-outcome.spec.ts` — all six tests passed in Chromium and WebKit.
- The complete CI suite was not run.

PR #267 exposed two regressions. A replacement reader could receive a queued old output-start mark and then the same mark again through retained-window replay. Mark emissions now carry a sequence; a subscriber that joined after a mark skips that queued copy and gets the replayed mark once, before retained rows. Ordinary shells that do not emit OSC 133 C now use the authenticated Start at row zero instead of waiting forever for a mark. The sequence change initially introduced a `rowMu`/runtime lock inversion; `DepartedRowCount` is now read before `rowMu` is acquired.

The protocol version remains 15; this change did not alter its value. The replay request remains on the existing per-attachment confirmation worker and adds no control-handler goroutine. The corrected implementation is validated and ready to re-offer from this branch.
