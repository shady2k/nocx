# Live session output-gap notification

- **Status:** implemented, with the bounded pane-local presentation correction below.
- **Bead:** `nocx-k6p18.29`.
- **Binding architecture:** AD-6, AD-7, AD-9 and AD-10; ADR-0066, ADR-0073, ADR-0078 and ADR-0081.
- **Owner decision already given (2026-10-09):** use a separate control-plane `session.outputGap` notification. The gap is a transport fact, not a session effect. Do not add an `outputGap` kind to `session.effect`, and do not rely on offsets added only to binary data frames.

## Problem

A helper-hosted session can fall behind the execution host's bounded output window. The helper reports a reset and the coordinator records the missing range, but the live output pump silently advances across that range. The tab then receives non-adjacent bytes as if they were continuous. Its byte cursor also stays behind the stream, so later acknowledgements do not name the offset at which the next bytes begin.

AD-10 requires a live reader whose cursor falls behind to receive an explicit reset carrying the gap. The recording path already has the range and reason. The live path does not.

## Solution

When a live subscriber crosses a host-window gap, it receives an ordered JSON-RPC `session.outputGap` notification with `sessionId`, `start`, `end`, and `reason`. `sessionId` is the server-minted session ID. `start` and `end` are non-negative JSON integers in the half-open byte range `[start, end)`, each no greater than `9007199254740991` (`Number.MAX_SAFE_INTEGER`); `reason` is the existing gap reason. The notification arrives after all earlier output bytes and before any later output bytes. The browser advances its byte cursor to `end` and acknowledges that offset before continuing. Raw payloads are counted and acknowledged, not decoded or retained as text (`nocx-58dpf`); the runtime's screen frames own visible output. The gap resets neither the backend emulator nor the screen model.

The live tab shows a warning in its pane that names the exact number of missing bytes and, when known, says the execution host's output window moved past the tab. It does not inject text into the terminal stream.

Presentation correction (`nocx-z5lnt`): each pane has at most one live-output
warning. It reports the cumulative union of missing byte ranges for the current
session binding, updating the same card rather than stacking cards. Known
host-window loss and unknown-cause loss each retain their own explanation;
mixed loss states both without attributing unknown bytes to the host.

The live card expires 8000 ms after mounting, or can be dismissed once. Updates
and duplicate ranges never extend that deadline. Expiry and dismissal remove
presentation only: the complete live ledger continues to exclude those bytes
from historical recovery loss. A genuinely new range after disappearance
mounts one new card with the cumulative count and a fresh deadline; duplicates
cannot resurrect it. Successful rebind and pane disposal cancel the old deadline
and reset the ledger and account. A failed or pending reconnect retains the
notice/account and its original expiry deadline, not the closed session binding.
Historical recovery cards and protocol-error danger toasts retain their
existing lifetimes.

### Client interpretation at delivery

**Owner decision (2026-10-10):** keep the exact four-field `session.outputGap` payload. The client classifies a valid gap against its current `state.offset`, the byte offset it expects next; the wire carries no delivery discriminator. First validate the session id and the payload's non-negative safe-integer offsets and non-empty range (`start < end`). If the session id names a known session, malformed or out-of-range bounds are a visible protocol error and stop later output for that session; they are not classified by offset relation. Notifications with no usable or known session id are ignored because there is no session stream to block.

For a structurally valid gap, apply exactly one of these cases:

1. **Informational, already crossed:** when `end <= state.offset`, surface the missing-output notice. Do not change the byte offset or send an acknowledgement. This covers a gap whose notification arrives after attach and whose entire range precedes the restored pane's current cursor.
2. **Ordered, at the cursor:** when `start === state.offset`, advance the cursor to `end`, acknowledge `end`, and surface the notice. The ordered notification remains between its preceding and following binary output.
3. **Inconsistent relation:** every other relation (including a gap ahead of the cursor or one that straddles it) is a visible session-level protocol error, not a log-only event. Surface it through the existing sticky danger toast. Stop applying later binary output for that session until it is reattached or otherwise recovered; do not change the cursor or acknowledge the invalid gap.

This classification does not weaken ordered delivery. Only a range already wholly at or behind the cursor is informational; a range at the expected cursor remains an ordered transition, and every ambiguous range is rejected visibly.

## Stories

1. **Live gap:** Given a live helper-hosted tab, when the helper's output window resets its reader, the tab reports the missing byte count, advances its acknowledgement cursor to the end of the gap, and receives later output at its correct offset. Cell rendering remains independent of raw-byte retention.
2. **No gap:** When output is adjacent, no gap notification or warning appears and the byte cursor advances only by received bytes.
3. **Backpressure:** If the ordered carrier cannot admit the gap notification, later output is not sent first. The pump waits for ordered capacity or ends that connection; it never silently drops the gap and continues the stream.
4. **Repeated range:** Re-delivery of the same range is idempotent for the warning and its byte count while the client's session state remains alive. A distinct later gap is reported as new missing output.
5. **Unknown reason:** The range is still reported exactly. The UI uses generic missing-output wording rather than guessing who caused it.
6. **Late informational gap:** When a restored pane attaches beyond a gap, the server replays the retained range for the session even if an earlier connection queued it. The client shows the notice but leaves its cursor and acknowledgements unchanged; repeated ranges are deduplicated within that client's session state because an ordered-queue admission is not proof of delivery.
7. **Malformed gap bounds:** When a malformed or unsafe gap names a known session, the client shows a protocol error and stops applying later output for that session. A missing or unknown session id is ignored.
8. **Inconsistent gap position:** When a valid gap is neither wholly at/before the cursor nor starts at it, the client shows a protocol error and stops applying that session's later output rather than guessing.
9. **Attach crosses a gap:** If `attach.from` advances beyond the requested recording frontier, use that byte cursor before the live stream continues. A following informational gap event leaves the cursor and acknowledgements unchanged.
10. **Bounded replay:** Retain at most 128 gap ranges per session and remember at most 128 distinct ranges per client session. At the server bound, future attach fails with an explicit error rather than resuming without replay history. At the client bound, show a protocol error and stop later output; neither side silently drops a range.
11. **Attach refused without proof of exit:** transport, replay-history and binding-recovery failures preserve the existing session identity. The pane pauses input and offers an explicit retry of that same attachment, not a replacement shell. Only an authoritative unknown-session verdict or a successful live inventory proving absence marks the binding lost (`nocx-220o6`). A registry entry without an available transport ring is unavailable, not absent. The 128-range refusal remains unchanged; retry does not erase that history or its safety bound.

## Decisions

### Already decided

- The gap uses its own JSON-RPC control-plane notification, `session.outputGap`, not `session.effect` and not synthetic terminal bytes. This is the owner's decision recorded on the bead.
- The backend remains the owner of terminal state and frames (AD-6 and ADR-0066). The gap notification does not clear the screen model or reset the emulator. The renderer has no raw-output text decoder or pending text queue.
- The server-minted session ID remains authoritative (AD-7). The notification is not a replayable visual frame or an effect.
- The payload has exactly `sessionId`, `start`, `end`, and `reason`. `start` and `end` are non-negative integer byte offsets in the existing session output coordinate, each bounded by `9007199254740991`; `start` is the first missing byte and `end` is the first byte after it. The missing count is `end - start`, computed exactly within the declared bound. `reason` remains a string so an unrecognized value is reported without guessing.
- If `attach.from` advances past the renderer's requested `attachAt`, adopt the returned cursor before the live stream continues. An informational `session.outputGap` that follows does not change the cursor or send an acknowledgement.
- The notification and session data share an ordered delivery path. While a connection is open, a full queue applies backpressure and later bytes cannot pass the notification. Queue admission is not a delivery receipt: the server retains at most 128 helper-gap ranges until the session closes and replays every applicable range on attach. At the 129th range, it marks history incomplete and refuses later attaches with an explicit JSON-RPC error. The client remembers at most 128 distinct ranges; a further distinct event raises a visible protocol error and stops output. If the socket closes, later bytes stop on that connection. A priority lane that can place the notification before earlier bytes is not sufficient unless the client stages by exact offset.
- Reuse the existing per-pane warning-card surface. Give the live case wording distinct from a reclaim notice. Do not add another status vocabulary or overlay the terminal.

### Coordinator decision (2026-10-10): keep JSON numbers with a safe ceiling

The coordinator default for 0.6 is to keep JSON numeric offsets; do not migrate to decimal strings or TypeScript `BigInt`. The owner may overrule this default. The ceiling is `9007199254740991` (`Number.MAX_SAFE_INTEGER`), inclusive. This is about 9 PB of output in one session, beyond practical use; a cross-contract migration would widen this focused transport task without a practical benefit.

Declare that maximum on the session-output byte-offset fields in every JSON schema `.29` creates or touches and in the existing fields it compares against:

- New `session.outputGap.start` and `.end`.
- `ack.params.offset`; `attach.params.offset` and `attach.from`.
- `sessions.live` and `workers.tabCreated` `replayFrom`.
- `session.output.params.from` and `session.output` `from`, `produced`, each `runs[].offset`, and each `gaps[].start/end`.
- `session.recoveryStatus.produced` and its `gaps[].start/end` (the shared `ledger.get` gap schema).

The JSON boundary must reject any inbound offset above the ceiling and refuse to emit an out-of-range result or notification through an explicit error path. Never truncate or continue the live stream with an imprecise offset. Add a Go boundary test proving the ceiling is accepted and `9007199254740992` fails explicitly. The internal helper protocol and its `uint64` cursor remain unchanged; they do not expose JSON numbers to the renderer. The frontend remains on `number`; no `BigInt` migration is in scope.

## Checks

- **Transport contract:** a real WebSocket test forces a host-window hole and validates the actual `session.outputGap` notification against its exact schema. It also proves the order: pre-gap binary data, gap notification, then post-gap binary data. A no-hole case sends no notification.
- **Frontend stream:** deliver arbitrary raw bytes without a text consumer. Verify the acknowledgement counts payload bytes, not decoded characters; screen frames and effects still arrive. Across an ordered gap the cursor jumps to the exact end, and the next acknowledgement names that end plus subsequent received bytes. An already-crossed gap shows its notice without changing the cursor or acknowledgements. An inconsistent gap shows a protocol error and stops later raw output without acknowledging it.
- **Visible behavior:** a burst of 32 distinct 32768-byte losses produces one stable live card reporting 1048576 missing bytes. Overlapping ranges count once. A fake clock proves expiry at 8000 ms despite an update at 4000 ms, and while a reconnect is rejected or still pending. Dismissal/expiry preserve historical exclusion, duplicates remain silent, and genuinely new loss remounts a cumulative card. Rebind, disposal, and independent panes retain their ownership boundaries. The authoritative screen frame is never cleared.
- **Browser acceptance:** the coordinator restart/reclaim flow in `e2e/remote-coordinator-reclaim.spec.ts` keeps the helper process running while the coordinator is absent until the actual helper window advances past the recorded cursor, then starts a fresh coordinator and restores the same pane. It asserts the real `session.outputGap` event with exact range/reason and captures the first visibly painted live card before unrelated restoration waits can outlast its deadline. That card's exact count must match a nonempty prefix union of received ranges. Historical ledger, restored block, continuing output, and session exit-status assertions remain separate. Do not use a sleep or add a production pause endpoint solely for this test.
- **Same-coordinator real-WebSocket acceptance:** while the coordinator remains up, deterministically hold the helper attachment reader (not browser delivery) behind the actual helper window, overflow that window, then release the reader. Assert the real `session.outputGap` arrives with exact `[start,end)` bounds and reason, ordered after preceding output and before later output. Use a test seam at the helper/transport boundary; do not add a production pause endpoint.
- The contract is generated and checked, the Go DTO conforms, and the over-the-wire tests validate both the attach-hole and same-coordinator notification paths. The Go boundary accepts `9007199254740991` and explicitly rejects `9007199254740992` without truncating or continuing the stream. The worker runs focused checks; the coordinator relies on CI for the full gate.
- On delivery failure, the test output and structured log identify the session and missing range. A log is not a substitute for the visible warning.

## Out

- Injecting nocx-authored text into terminal bytes.
- Adding an effect kind or changing the binary data-frame format.
- Clearing or reconstructing the backend emulator or the cell-painted screen.
- Changing output-window sizing, recording retention, or the session's history policy.
- Removing xterm.js; that remains the separate `.8` stage. This change must not depend on the future cutover.
- Assigning an ADR number before the outstanding `.8` collision is resolved.

## DONE WHEN

A person can resume a helper-hosted tab after an execution-host output-window reset, see the exact missing-byte count in the pane, and receive later output with its byte cursor and acknowledgement at the correct stream offset. The browser acceptance proves the restored pane receives the live gap notice; the same-coordinator real-WebSocket acceptance proves the gap notification is ordered correctly while the coordinator remains up.
