# Report — nocx-xn63t.6.19, iteration 3

## Invariant

The rows a reader was owed cross the wire by the recovery attach's own replay, which serializes output mark, rows and interval end; the mark never triggers a second same-subscriber attach and never confirms a span that is not durable.

## Failure and fix

CI showed the restart acceptance sealing 23 of 300 rows, with `block close: the artifact is not at the interval`, and showed a replacement reader receive `[output-start output-start rows rows]`. The ordinary block-action, completion and notification e2e specs also regressed.

An output-start mark is a position, not proof that preceding rows are durable. Under load, its row can trail the artifact cursor. In the local failing acceptance log, the last persisted append ended at row 177 (`from=150 rows=27 upTo=177`). The close then arrived with `endRow=277 cursor=177`, and the artifact sealed with 200 rows. The owed span `[177,277)` was not in the block artifact at close; the only 23 rows after the close warning were the closing screen. Those owed rows remained in the helper's retained window while the coordinator was away. They were not durable in the block store yet.

The recovery attach already requests and orders the helper's retained-window replay. The previous code also requested a second same-subscriber attach when it saw the output mark. That could replay duplicate marks and let the late mark act as a boundary beyond the durable cursor. The fix removes that extra attach. A fresh authenticated block ignores output marks and uses row zero. A recovered block may use its mark, but its effective boundary is clamped to the durable cursor, including when the cursor is zero. Rows held before the replay fence are stored before the replayed suffix. A mark cannot confirm farther than the durable cursor. The helper advances each subscriber's output-mark sequence when it sends the replay mark, so an older queued mark cannot arrive a second time after retained rows.

The history-page carrier ordering failure found in the full package run is independent. Its binary carrier used the refreshable queue while its JSON-RPC response used the priority response queue. A separate commit (nocx-xomj4) sends the carrier through the existing reserved response FIFO before the result, without changing refreshable-queue policy.

## Evidence

- Local red before the fix: the selected chromium container run had 10/11 pass; notification grouping never showed `build three chromium ×3`. The restarted-block acceptance logged cursor 177, end row 277, and a short seal.
- Coordinator-restart acceptance: 10/10 under six CPU hogs, while `./internal/app`, `./internal/helper/session`, and `./internal/transport` suites ran concurrently.
- Ordinary-path container guard: the selected chromium specs passed 11/11 twice, including notification grouping.
- Zero-live-retention container spec: `e2e/transcript-scroll-budget.spec.ts` passed 2/2. Its zero-retention case verified 5000 rows after restart and no live history.
- The reader replay ordering test and the new empty-durable-cursor test pass in the focused transport run.
- `go vet -tags gtk3 ./internal/app/ ./internal/helper/session/ ./internal/transport/` passed.
- Focused post-fix transport tests passed, including the reader replay ordering test and the zero-cursor recovery regression.
- The full transport `-race -count=1` run timed out at 10m in `TestSessionRecording_DetachedOutputIsOnDiskAfterwards`, with many `control.orderedSubmission.worker` goroutines. The same test passed in isolation under `-race` in 1.4s. This suite-level hang is being checked against `origin/main` under separate bead `nocx-rq2xn` and is not reported as green.

The local output order in the failing run shows an early close relative to durable storage, not a history-page response overtaking a block-row document. The owed span was absent from the artifact. The zero-retention replay test is the evidence that the initial recovery attach still transfers it into durable storage before the interval seals.
