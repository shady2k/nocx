# Live session output-gap notification

- **Status:** ready for implementation; numeric-offset ceiling is the coordinator default for 0.6 and may be overruled by the owner.
- **Bead:** `nocx-k6p18.29`.
- **Binding architecture:** AD-6, AD-7, AD-9 and AD-10; ADR-0066, ADR-0073, ADR-0078 and ADR-0081.
- **Owner decision already given (2026-10-09):** use a separate control-plane `session.outputGap` notification. The gap is a transport fact, not a session effect. Do not add an `outputGap` kind to `session.effect`, and do not rely on offsets added only to binary data frames.

## Problem

A helper-hosted session can fall behind the execution host's bounded output window. The helper reports a reset and the coordinator records the missing range, but the live output pump silently advances across that range. The tab then receives non-adjacent bytes as if they were continuous. Its byte cursor also stays behind the stream, so later acknowledgements do not name the offset at which the next bytes begin.

AD-10 requires a live reader whose cursor falls behind to receive an explicit reset carrying the gap. The recording path already has the range and reason. The live path does not.

## Solution

When a live subscriber crosses a host-window gap, it receives an ordered JSON-RPC `session.outputGap` notification with `sessionId`, `start`, `end`, and `reason`. `sessionId` is the server-minted session ID. `start` and `end` are non-negative JSON integers in the half-open byte range `[start, end)`, each no greater than `9007199254740991` (`Number.MAX_SAFE_INTEGER`); `reason` is the existing gap reason. The notification arrives after all earlier output bytes and before any later output bytes. The browser client resets its `UTF8StreamDecoder`, advances its byte cursor to `end`, and acknowledges that offset before continuing. It does not reset the hidden xterm parser, backend emulator, or screen model.

The live tab shows a warning in its pane that names the exact number of missing bytes and, when known, says the execution host's output window moved past the tab. It does not inject text into the terminal stream.

## Stories

1. **Live gap:** Given a live helper-hosted tab, when the helper's output window resets its reader, the tab reports the missing byte count, drops decoder state from before the gap, advances its acknowledgement cursor to the end of the gap, and receives later output without joining bytes across the hole.
2. **No gap:** When output is adjacent, no gap notification or warning appears and the byte cursor advances only by received bytes.
3. **Backpressure:** If the ordered carrier cannot admit the gap notification, later output is not sent first. The pump waits for ordered capacity or ends that connection; it never silently drops the gap and continues the stream.
4. **Repeated range:** Re-delivery of the same range is idempotent for the warning and its byte count. A distinct later gap is reported as new missing output.
5. **Unknown reason:** The range is still reported exactly. The UI uses generic missing-output wording rather than guessing who caused it.

## Decisions

### Already decided

- The gap uses its own JSON-RPC control-plane notification, `session.outputGap`, not `session.effect` and not synthetic terminal bytes. This is the owner's decision recorded on the bead.
- The backend remains the owner of terminal state and frames (AD-6 and ADR-0066). The gap notification does not clear the screen model or reset the emulator. It resets only the renderer's streaming UTF-8 decoder, matching the existing recording reader's behavior across non-adjacent byte runs.
- The server-minted session ID remains authoritative (AD-7). The notification is not a replayable visual frame or an effect.
- The payload has exactly `sessionId`, `start`, `end`, and `reason`. `start` and `end` are non-negative integer byte offsets in the existing session output coordinate, each bounded by `9007199254740991`; `start` is the first missing byte and `end` is the first byte after it. The missing count is `end - start`, computed exactly within the declared bound. `reason` remains a string so an unrecognized value is reported without guessing.
- The notification and session data must share an ordered delivery path. Its admission is non-droppable: a full queue applies backpressure; an unrecoverable connection failure stops later bytes on that connection. A priority lane that can place the notification before earlier bytes is not sufficient unless the client stages by exact offset.
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
- **Frontend stream:** deliver a multi-byte UTF-8 character split by a gap. Verify its prefix is discarded, post-gap bytes are not joined to it, the live cursor jumps to the exact end, and the next acknowledgement names that end plus subsequent received bytes.
- **Visible behavior:** a live pane shows the warning with the exact missing-byte count, does not clear its authoritative screen frame, and dismisses the card without changing the stream. A later distinct gap is reported again.
- **Browser acceptance:** keep a real helper-hosted pane attached while a deterministic gate holds its reader behind the actual reported window; release enough remote output to cause the real reset. Verify the warning, gap ordering, and later output in the live tab. Do not use a sleep or add a production pause endpoint solely for this test.
- The contract is generated and checked, the Go DTO conforms, and the over-the-wire test validates the real notification. The Go boundary accepts `9007199254740991` and explicitly rejects `9007199254740992` without truncating or continuing the stream. The worker runs focused checks; the coordinator relies on CI for the full gate.
- On delivery failure, the test output and structured log identify the session and missing range. A log is not a substitute for the visible warning.

## Out

- Injecting nocx-authored text into terminal bytes.
- Adding an effect kind or changing the binary data-frame format.
- Clearing or reconstructing the backend emulator or the cell-painted screen.
- Changing output-window sizing, recording retention, or the session's history policy.
- Removing xterm.js; that remains the separate `.8` stage. This change must not depend on the future cutover.
- Assigning an ADR number before the outstanding `.8` collision is resolved.

## DONE WHEN

A person can keep watching a helper-hosted tab through an execution-host output-window reset, see the exact missing-byte count in the pane, and then receive later output with its byte cursor and acknowledgement at the correct stream offset. A real browser acceptance check watches that happen, and a real-socket contract check validates the notification that carries the gap.
